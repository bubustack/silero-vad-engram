package engram

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "unsafe"

	"github.com/bubustack/bobrapet/pkg/storage"
	sdk "github.com/bubustack/bubu-sdk-go"
	sdkengram "github.com/bubustack/bubu-sdk-go/engram"
	"github.com/bubustack/bubu-sdk-go/media"
	"github.com/bubustack/bubu-sdk-go/runtime"
	cfgpkg "github.com/bubustack/silero-vad-engram/pkg/config"
	"github.com/bubustack/tractatus/transport"
	"github.com/streamer45/silero-vad-go/speech"
)

const (
	bytesPerSample    = 2
	audioPacketType   = transport.StreamTypeSpeechAudio
	outputModePacket  = "packet"
	outputModeAudio   = "audio"
	outputEncodingPCM = "pcm"
	outputEncodingWAV = "wav"
)

var emitSignalFunc = sdk.EmitSignal

type SileroVAD struct {
	cfg         cfgpkg.Config
	secrets     *sdkengram.Secrets
	storyInfo   sdkengram.StoryInfo
	storageOnce sync.Once
	storage     *storage.StorageManager
	storageErr  error
}

type vadStreamContext struct {
	engine          *SileroVAD
	logger          *slog.Logger
	out             chan<- sdkengram.StreamMessage
	state           *streamState
	coalesceDur     time.Duration
	coalesceMax     time.Duration
	coalesceBuffers map[string]*coalesceBuffer
	sawAudio        bool
	sawBinary       bool
}

func New() *SileroVAD {
	return &SileroVAD{}
}

func (e *SileroVAD) Init(ctx context.Context, cfg cfgpkg.Config, secrets *sdkengram.Secrets) error {
	e.cfg = cfgpkg.Normalize(cfg)
	e.secrets = secrets

	if execCtx, err := runtime.LoadExecutionContextData(); err == nil && execCtx != nil {
		e.storyInfo = execCtx.StoryInfo
	} else if err != nil {
		sdk.LoggerFromContext(ctx).Warn("silero-vad: failed to load execution context metadata", "error", err)
	}
	return nil
}

func (e *SileroVAD) Stream(
	ctx context.Context,
	in <-chan sdkengram.InboundMessage,
	out chan<- sdkengram.StreamMessage,
) error {
	logger := sdk.LoggerFromContext(ctx).With("component", "silero-vad")
	stream := newVADStreamContext(e, logger, out)
	defer stream.close()

	for {
		select {
		case <-ctx.Done():
			if err := stream.flushPending(ctx); err != nil {
				return err
			}
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return stream.flushPending(ctx)
			}
			stream.handleMessage(ctx, msg)
		}
	}
}

func newVADStreamContext(
	engine *SileroVAD,
	logger *slog.Logger,
	out chan<- sdkengram.StreamMessage,
) *vadStreamContext {
	return &vadStreamContext{
		engine:          engine,
		logger:          logger,
		out:             out,
		state:           newStreamState(engine.cfg, logger),
		coalesceDur:     time.Duration(engine.cfg.CoalesceDurationMs) * time.Millisecond,
		coalesceMax:     time.Duration(engine.cfg.CoalesceMaxDurationMs) * time.Millisecond,
		coalesceBuffers: make(map[string]*coalesceBuffer),
	}
}

func (s *vadStreamContext) close() {
	for _, cb := range s.coalesceBuffers {
		cb.flush()
		cb.stop()
	}
	s.state.close()
}

func (s *vadStreamContext) handleMessage(
	ctx context.Context,
	msg sdkengram.InboundMessage,
) {
	s.observeFirstFrames(msg)
	pkt, ok, err := packetFromStreamMessage(msg)
	if err != nil {
		s.logger.Error("failed to parse audio packet", "error", err)
		msg.Done()
		return
	}
	if !ok {
		s.handleEmptyMessage(msg)
		return
	}
	if !isSupportedAudioPacket(pkt.Type) {
		s.engine.logIgnoredPacket(ctx, s.logger, &pkt, "non-audio type")
		msg.Done()
		return
	}
	if len(pkt.Audio.Data) == 0 {
		s.engine.logIgnoredPacket(ctx, s.logger, &pkt, "empty audio payload")
		msg.Done()
		return
	}

	normalizeAudioPacket(&pkt)
	s.engine.logDetectorInput(ctx, s.logger, &pkt)
	chunks, err := s.state.ingest(pkt)
	if err != nil {
		s.logger.Error("failed to process audio", "error", err)
		msg.Done()
		return
	}
	participantKey := resolveParticipantKey(pkt.Participant)
	for _, chunk := range chunks {
		s.getCoalesceBuffer(ctx, participantKey, pkt.Room, pkt.Participant).add(chunk)
	}
	msg.Done()
}

func (s *vadStreamContext) observeFirstFrames(msg sdkengram.InboundMessage) {
	if !s.sawAudio && msg.Audio != nil && len(msg.Audio.PCM) > 0 {
		s.logger.Info(
			"received first audio frame",
			"pcmLen", len(msg.Audio.PCM),
			"sampleRate", msg.Audio.SampleRateHz,
			"channels", msg.Audio.Channels,
			"codec", msg.Audio.Codec,
		)
		s.sawAudio = true
	}
	if !s.sawBinary && msg.Binary != nil && len(msg.Binary.Payload) > 0 {
		s.logger.Info(
			"received first binary frame",
			"mimeType", msg.Binary.MimeType,
			"payloadLen", len(msg.Binary.Payload),
		)
		s.sawBinary = true
	}
}

func (s *vadStreamContext) handleEmptyMessage(msg sdkengram.InboundMessage) {
	if isHeartbeat(msg.Metadata) {
		msg.Done()
		return
	}
	s.logger.Warn("ignoring message without payload", "metadata", msg.Metadata)
	msg.Done()
}

func (s *vadStreamContext) getCoalesceBuffer(
	ctx context.Context,
	key string,
	room roomInfo,
	participant participantInfo,
) *coalesceBuffer {
	if cb, ok := s.coalesceBuffers[key]; ok {
		return cb
	}

	cb := newCoalesceBuffer(s.coalesceDur, s.coalesceMax, func(merged turnChunk) {
		seq := s.state.nextSequence(key)
		durationMs := calcDurationMs(len(merged.PCM), merged.SampleRate, merged.Channels)
		s.logger.Info("emitting coalesced VAD turn",
			"participant", key,
			"sequence", seq,
			"bytes", len(merged.PCM),
			"durationMs", durationMs,
		)
		s.state.logOutputPCMStats(key, merged.PCM, merged.SampleRate, merged.Channels)
		_ = s.engine.sendTurn(ctx, s.logger, s.out, room, participant, merged, seq)
	})
	s.coalesceBuffers[key] = cb
	return cb
}

func (s *vadStreamContext) flushPending(ctx context.Context) error {
	for _, cb := range s.coalesceBuffers {
		cb.flush()
	}
	return s.state.flushAll(func(key string, ctxInfo participantContext, chunk turnChunk, sequence int) error {
		participant := ctxInfo.participant
		if participant.Identity == "" {
			participant.Identity = key
		}
		s.state.logOutputPCMStats(key, chunk.PCM, chunk.SampleRate, chunk.Channels)
		return s.engine.sendTurn(ctx, s.logger, s.out, ctxInfo.room, participant, chunk, sequence)
	})
}

func resolveParticipantKey(participant participantInfo) string {
	if participant.Identity != "" {
		return participant.Identity
	}
	if participant.SID != "" {
		return participant.SID
	}
	return "default"
}

func normalizeAudioPacket(pkt *audioPacket) {
	if pkt == nil {
		return
	}
	if pkt.Audio.SampleRate <= 0 {
		pkt.Audio.SampleRate = 48000
	}
	if pkt.Audio.Channels <= 0 {
		pkt.Audio.Channels = 1
	}
}

func packetFromStreamMessage(msg sdkengram.InboundMessage) (audioPacket, bool, error) {
	if msg.Audio == nil || len(msg.Audio.PCM) == 0 {
		raw := streamStructuredBytes(msg)
		if len(raw) == 0 {
			return audioPacket{}, false, nil
		}
		var pkt audioPacket
		if err := json.Unmarshal(raw, &pkt); err != nil {
			return audioPacket{}, false, err
		}
		return pkt, true, nil
	}
	meta := msg.Metadata
	packetType := meta["type"]
	if packetType == "" || !strings.EqualFold(packetType, audioPacketType) {
		// AudioFrame is authoritative; normalize to the expected audio packet type.
		packetType = audioPacketType
	}
	pkt := audioPacket{
		Type: packetType,
		Room: roomInfo{
			Name: meta["room.name"],
			SID:  meta["room.sid"],
		},
		Participant: participantInfo{
			Identity: meta["participant.id"],
			SID:      meta["participant.sid"],
		},
		Audio: audioBuffer{
			Encoding:   strings.ToLower(msg.Audio.Codec),
			SampleRate: int(msg.Audio.SampleRateHz),
			Channels:   int(msg.Audio.Channels),
			Data:       msg.Audio.PCM,
		},
	}
	if pkt.Audio.Encoding == "" {
		pkt.Audio.Encoding = outputEncodingPCM
	}
	return pkt, true, nil
}

func streamStructuredBytes(msg sdkengram.InboundMessage) []byte {
	if len(msg.Inputs) > 0 {
		return msg.Inputs
	}
	if len(msg.Payload) > 0 {
		return msg.Payload
	}
	if msg.Binary != nil && len(msg.Binary.Payload) > 0 {
		return msg.Binary.Payload
	}
	return nil
}

func (e *SileroVAD) storageManager(ctx context.Context) *storage.StorageManager {
	e.storageOnce.Do(func() {
		sm, err := storage.SharedManager(ctx)
		if err != nil {
			e.storageErr = err
			return
		}
		e.storage = sm
	})
	if e.storageErr != nil {
		return nil
	}
	return e.storage
}

func (e *SileroVAD) sendTurn(
	ctx context.Context,
	logger *slog.Logger,
	out chan<- sdkengram.StreamMessage,
	room roomInfo,
	participant participantInfo,
	chunk turnChunk,
	sequence int,
) error {
	if logger == nil {
		logger = sdk.LoggerFromContext(ctx)
	}
	chunk = e.resampleTurnChunk(logger, chunk)
	outputMode := resolveOutputMode(e.cfg.OutputMode)
	payloadAudio := e.buildPayloadAudio(logger, outputMode, chunk)
	logger.Info(
		"emitting VAD turn",
		"sampleRate", payloadAudio.SampleRate,
		"channels", payloadAudio.Channels,
		"encoding", payloadAudio.Encoding,
		"detectorSampleRate", e.cfg.DetectorSampleRate,
	)

	if outputMode != outputModeAudio {
		if err := e.maybeOffloadTurnAudio(ctx, logger, room, participant, &payloadAudio); err != nil {
			return err
		}
	}

	durationMs := calcDurationMs(len(chunk.PCM), chunk.SampleRate, chunk.Channels)
	packet := turnPacket{
		Type:        transport.StreamTypeSpeechVADActive,
		Room:        room,
		Participant: participant,
		Sequence:    sequence,
		DurationMs:  durationMs,
		Timestamp:   time.Now().UTC(),
		Audio:       payloadAudio,
	}
	e.logDetectorOutput(ctx, logger, &packet)
	if err := e.emitTurnMessage(ctx, out, outputMode, participant.Identity, sequence, payloadAudio, packet); err != nil {
		return err
	}
	e.emitTurnSignal(ctx, logger, room, participant, payloadAudio, sequence, durationMs)
	return nil
}

func (e *SileroVAD) resampleTurnChunk(
	logger *slog.Logger,
	chunk turnChunk,
) turnChunk {
	if e.cfg.OutputSampleRateHz <= 0 || e.cfg.OutputSampleRateHz == chunk.SampleRate {
		return chunk
	}
	if chunk.Channels != 1 {
		logger.Warn(
			"output resample requested but channels != 1; skipping",
			"requestedSampleRate", e.cfg.OutputSampleRateHz,
			"inputSampleRate", chunk.SampleRate,
			"channels", chunk.Channels,
		)
		return chunk
	}

	int16pcm, err := bytesToInt16LE(chunk.PCM)
	if err != nil {
		logger.Warn(
			"failed to decode PCM for resample; keeping original",
			"inputSampleRate", chunk.SampleRate,
			"error", err,
		)
		return chunk
	}
	resampled, err := resampleInt16Mono(int16pcm, chunk.SampleRate, e.cfg.OutputSampleRateHz)
	if err != nil {
		logger.Warn(
			"failed to resample output audio; keeping original",
			"requestedSampleRate", e.cfg.OutputSampleRateHz,
			"inputSampleRate", chunk.SampleRate,
			"error", err,
		)
		return chunk
	}
	chunk.PCM = int16ToBytesLE(resampled)
	chunk.SampleRate = e.cfg.OutputSampleRateHz
	return chunk
}

func resolveOutputMode(value string) string {
	outputMode := strings.ToLower(strings.TrimSpace(value))
	if outputMode == "" {
		return outputModePacket
	}
	return outputMode
}

func (e *SileroVAD) buildPayloadAudio(
	logger *slog.Logger,
	outputMode string,
	chunk turnChunk,
) audioBuffer {
	payloadAudio := audioBuffer{
		Encoding:   strings.ToLower(e.cfg.OutputEncoding),
		SampleRate: chunk.SampleRate,
		Channels:   chunk.Channels,
		Data:       chunk.PCM,
	}
	if outputMode == outputModeAudio {
		if payloadAudio.Encoding != outputEncodingPCM && payloadAudio.Encoding != "" {
			logger.Warn(
				"audio output ignores non-PCM encoding; forcing pcm",
				"encoding", payloadAudio.Encoding,
			)
		}
		payloadAudio.Encoding = outputEncodingPCM
		return payloadAudio
	}
	if payloadAudio.Encoding == outputEncodingWAV {
		payloadAudio.Data = wrapPCMAsWAV(chunk.PCM, chunk.SampleRate, chunk.Channels)
	}
	return payloadAudio
}

func (e *SileroVAD) maybeOffloadTurnAudio(
	ctx context.Context,
	logger *slog.Logger,
	room roomInfo,
	participant participantInfo,
	payloadAudio *audioBuffer,
) error {
	if payloadAudio == nil || e.cfg.InlineAudioLimit <= 0 {
		return nil
	}
	if len(payloadAudio.Data) <= e.cfg.InlineAudioLimit {
		return nil
	}
	if e.storageManager(ctx) == nil {
		if e.storageErr != nil {
			return fmt.Errorf(
				"audio turn exceeds inline limit %d and shared storage is unavailable: %w",
				e.cfg.InlineAudioLimit,
				e.storageErr,
			)
		}
		return fmt.Errorf(
			"audio turn exceeds inline limit %d and shared storage is unavailable",
			e.cfg.InlineAudioLimit,
		)
	}

	scope := buildTurnScope(room, participant)
	ref, err := media.MaybeOffloadBlob(ctx, e.storage, payloadAudio.Data, media.WriteOptions{
		Namespace:   room.SID,
		StoryRun:    strings.TrimSpace(e.storyInfo.StoryRunID),
		Step:        strings.TrimSpace(e.storyInfo.StepName),
		Scope:       scope,
		ContentType: offloadContentType(payloadAudio.Encoding),
		InlineLimit: e.cfg.InlineAudioLimit,
	})
	if err == nil && ref != nil {
		e.logStorageDecision(ctx, logger, participant.Identity, true, len(payloadAudio.Data), nil)
		payloadAudio.Storage = ref
		payloadAudio.Data = nil
		return nil
	}
	if err != nil {
		e.logStorageDecision(ctx, logger, participant.Identity, false, len(payloadAudio.Data), err)
	}
	return nil
}

func buildTurnScope(room roomInfo, participant participantInfo) []string {
	scope := []string{}
	if participant.Identity != "" {
		scope = append(scope, participant.Identity)
	}
	if participant.SID != "" {
		scope = append(scope, participant.SID)
	}
	if room.Name != "" {
		scope = append(scope, room.Name)
	}
	return scope
}

func (e *SileroVAD) emitTurnMessage(
	ctx context.Context,
	out chan<- sdkengram.StreamMessage,
	outputMode string,
	participant string,
	sequence int,
	payloadAudio audioBuffer,
	packet turnPacket,
) error {
	metadata := map[string]string{
		"source":      "silero",
		"type":        transport.StreamTypeSpeechVADActive,
		"provider":    "silero",
		"participant": participant,
		"sequence":    strconv.Itoa(sequence),
	}
	if outputMode == outputModeAudio {
		select {
		case out <- sdkengram.StreamMessage{
			Audio: &sdkengram.AudioFrame{
				PCM:          payloadAudio.Data,
				SampleRateHz: int32(payloadAudio.SampleRate),
				Channels:     int32(payloadAudio.Channels),
				Codec:        payloadAudio.Encoding,
			},
			Metadata: metadata,
		}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	body, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	select {
	case out <- sdkengram.StreamMessage{
		Payload: body,
		Binary: &sdkengram.BinaryFrame{
			Payload:  body,
			MimeType: "application/json",
		},
		Metadata: metadata,
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func offloadContentType(encoding string) string {
	if strings.EqualFold(strings.TrimSpace(encoding), "wav") {
		return "audio/wav"
	}
	return "audio/L16"
}

func (e *SileroVAD) emitTurnSignal(
	ctx context.Context,
	logger *slog.Logger,
	room roomInfo,
	participant participantInfo,
	audio audioBuffer,
	sequence, durationMs int,
) {
	payload := map[string]any{
		"type":       "speech.turn.v1",
		"sequence":   sequence,
		"durationMs": durationMs,
	}
	if participant.Identity != "" {
		payload["participant"] = participant.Identity
	}
	if participant.SID != "" {
		payload["participantSid"] = participant.SID
	}
	if room.Name != "" {
		payload["room"] = room.Name
	}
	if room.SID != "" {
		payload["roomSid"] = room.SID
	}
	if audio.SampleRate > 0 {
		payload["sampleRate"] = audio.SampleRate
	}
	if audio.Channels > 0 {
		payload["channels"] = audio.Channels
	}
	if audio.Encoding != "" {
		payload["encoding"] = audio.Encoding
	}
	if audio.Storage != nil {
		payload["storage"] = audio.Storage
	}
	if logger == nil {
		logger = sdk.LoggerFromContext(ctx)
	}
	if err := emitSignalFunc(ctx, "speech.turn.v1", payload); err != nil && !errors.Is(err, sdk.ErrSignalsUnavailable) {
		logger.Warn("Failed to emit Silero VAD turn signal", "error", err)
	}
}

type streamState struct {
	cfg       cfgpkg.Config
	logger    *slog.Logger
	speakers  map[string]*speakerState
	contexts  map[string]participantContext
	seq       map[string]int
	debugLog  map[string]int
	outputLog map[string]int
}

func newStreamState(cfg cfgpkg.Config, logger *slog.Logger) *streamState {
	return &streamState{
		cfg:       cfg,
		logger:    logger,
		speakers:  make(map[string]*speakerState),
		contexts:  make(map[string]participantContext),
		seq:       make(map[string]int),
		debugLog:  make(map[string]int),
		outputLog: make(map[string]int),
	}
}

func (s *streamState) speakerKey(pkt audioPacket) string {
	if pkt.Participant.Identity != "" {
		return pkt.Participant.Identity
	}
	if pkt.Participant.SID != "" {
		return pkt.Participant.SID
	}
	return "default"
}

func (s *streamState) speaker(pkt audioPacket) (*speakerState, error) {
	key := s.speakerKey(pkt)
	if existing, ok := s.speakers[key]; ok {
		return existing, nil
	}
	s.logger.Info(
		"creating speaker state",
		"participant", key,
		"inputSampleRate", pkt.Audio.SampleRate,
		"channels", pkt.Audio.Channels,
		"detectorSampleRate", s.cfg.DetectorSampleRate,
		"outputSampleRate", s.cfg.OutputSampleRateHz,
	)
	cls, err := newSpeakerState(s.cfg, pkt.Audio.SampleRate, pkt.Audio.Channels)
	if err != nil {
		return nil, err
	}
	s.speakers[key] = cls
	return cls, nil
}

func (s *streamState) ingest(pkt audioPacket) ([]turnChunk, error) {
	key := s.speakerKey(pkt)
	s.contexts[key] = participantContext{
		room:        pkt.Room,
		participant: pkt.Participant,
	}
	s.logPCMStats(key, pkt.Audio.Data, pkt.Audio.SampleRate, pkt.Audio.Channels)
	speaker, err := s.speaker(pkt)
	if err != nil {
		return nil, err
	}
	rawChunks, err := speaker.ingest(pkt.Audio.Data)
	if err != nil {
		return nil, err
	}
	if len(rawChunks) == 0 {
		return nil, nil
	}
	chunks := make([]turnChunk, len(rawChunks))
	for i, chunk := range rawChunks {
		chunks[i] = turnChunk{
			PCM:        chunk,
			SampleRate: pkt.Audio.SampleRate,
			Channels:   pkt.Audio.Channels,
		}
	}
	return chunks, nil
}

func (s *streamState) logPCMStats(key string, data []byte, sampleRate, channels int) {
	if s == nil || s.logger == nil {
		return
	}
	const maxDebugFrames = 200
	if s.debugLog[key] >= maxDebugFrames {
		return
	}
	s.debugLog[key]++
	rms, peak := pcmStatsInt16(data)
	s.logger.Info(
		"pcm stats",
		"participant", key,
		"sampleRate", sampleRate,
		"channels", channels,
		"rms", fmt.Sprintf("%.2f", rms),
		"peak", peak,
		"bytes", len(data),
	)
}

func (s *streamState) logOutputPCMStats(key string, data []byte, sampleRate, channels int) {
	if s == nil || s.logger == nil {
		return
	}
	const maxDebugFrames = 200
	if s.outputLog[key] >= maxDebugFrames {
		return
	}
	s.outputLog[key]++
	rms, peak := pcmStatsInt16(data)
	s.logger.Info(
		"pcm output stats",
		"participant", key,
		"sampleRate", sampleRate,
		"channels", channels,
		"rms", fmt.Sprintf("%.2f", rms),
		"peak", peak,
		"bytes", len(data),
	)
}

func (s *streamState) flushAll(
	handler func(key string, ctx participantContext, chunk turnChunk, sequence int) error,
) error {
	for speakerID, speaker := range s.speakers {
		chunks := speaker.flush()
		ctxInfo := s.contexts[speakerID]
		for _, chunk := range chunks {
			seq := s.nextSequence(speakerID)
			payloadChunk := turnChunk{PCM: chunk, SampleRate: speaker.sampleRate, Channels: speaker.channels}
			if err := handler(speakerID, ctxInfo, payloadChunk, seq); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *streamState) nextSequence(participant string) int {
	s.seq[participant]++
	return s.seq[participant]
}

func (s *streamState) close() {
	for _, speaker := range s.speakers {
		speaker.close()
	}
}

type speakerState struct {
	buffer     *turnBuffer
	cls        frameClassifier
	sampleRate int
	channels   int
}

func newSpeakerState(cfg cfgpkg.Config, sampleRate, channels int) (*speakerState, error) {
	cls, err := newSileroClassifier(cfg)
	if err != nil {
		return nil, err
	}
	buf, err := newTurnBuffer(cls, cfg, sampleRate, channels)
	if err != nil {
		_ = cls.Close()
		return nil, err
	}
	return &speakerState{buffer: buf, cls: cls, sampleRate: sampleRate, channels: channels}, nil
}

func (s *speakerState) ingest(data []byte) ([][]byte, error) {
	return s.buffer.ingest(data)
}

func (s *speakerState) flush() [][]byte {
	return s.buffer.flushForce()
}

func (s *speakerState) close() {
	_ = s.cls.Close()
}

type frameClassifier interface {
	IsSpeech(frame []byte, sampleRate, channels int) (bool, error)
	Close() error
}

type sileroClassifier struct {
	detector     *speech.Detector
	targetRate   int
	threshold    float32
	windowSize   int
	floatBuf     []float32
	lastDecision bool
	debugCount   int
}

func newSileroClassifier(cfg cfgpkg.Config) (*sileroClassifier, error) {
	detector, err := speech.NewDetector(speech.DetectorConfig{
		ModelPath:            cfg.ModelPath,
		SampleRate:           cfg.DetectorSampleRate,
		Threshold:            cfg.Threshold,
		MinSilenceDurationMs: 0,
		SpeechPadMs:          0,
		LogLevel:             speech.LogLevelWarn,
	})
	if err != nil {
		return nil, err
	}
	window := 512
	if cfg.DetectorSampleRate == 8000 {
		window = 256
	}
	return &sileroClassifier{
		detector:   detector,
		targetRate: cfg.DetectorSampleRate,
		threshold:  cfg.Threshold,
		windowSize: window,
		floatBuf:   make([]float32, 0, window*2),
	}, nil
}

func (s *sileroClassifier) IsSpeech(frame []byte, sampleRate, channels int) (bool, error) {
	mono, err := downmixToMono(frame, channels)
	if err != nil {
		return false, err
	}
	mono = applyAGC(mono, s.debugCount)
	resampled, err := resampleInt16Mono(mono, sampleRate, s.targetRate)
	if err != nil {
		return false, err
	}
	floats := int16ToFloat32(resampled)
	s.floatBuf = append(s.floatBuf, floats...)

	produced := false
	for len(s.floatBuf) >= s.windowSize {
		prob, err := detectorInfer(s.detector, s.floatBuf[:s.windowSize])
		if err != nil {
			return false, err
		}
		if s.debugCount < 200 {
			slog.Info(
				"silero vad prob",
				"prob", prob,
				"threshold", s.threshold,
				"targetSampleRate", s.targetRate,
			)
			s.debugCount++
		}
		s.floatBuf = s.floatBuf[s.windowSize:]
		s.lastDecision = prob >= s.threshold
		produced = true
	}
	if !produced {
		return s.lastDecision, nil
	}
	return s.lastDecision, nil
}

func (s *sileroClassifier) Close() error {
	if s.detector == nil {
		return nil
	}
	return s.detector.Destroy()
}

//go:linkname detectorInfer github.com/streamer45/silero-vad-go/speech.(*Detector).infer
func detectorInfer(sd *speech.Detector, samples []float32) (float32, error)

// --- turn buffer (adapted) ---
type turnBuffer struct {
	classifier        frameClassifier
	sampleRate        int
	channels          int
	frameDurationMs   int
	frameBytes        int
	frameSamples      int
	silenceSamplesReq int
	minSamples        int
	maxSamples        int

	rawData            []byte
	analysisBuf        []byte
	bytesConsumed      int
	processedSamples   int
	samplesSinceSpeech int
	inSpeech           bool
	hadSpeech          bool
}

func newTurnBuffer(classifier frameClassifier, cfg cfgpkg.Config, sampleRate, channels int) (*turnBuffer, error) {
	frameBytes := frameSizeBytes(cfg.FrameDurationMs, sampleRate, channels)
	if frameBytes <= 0 {
		return nil, fmt.Errorf("unsupported frame duration %dms for sample rate %d", cfg.FrameDurationMs, sampleRate)
	}
	frameSamples := frameBytes / (channels * bytesPerSample)
	silenceFrames := max(1, cfg.MinSilenceDuration/cfg.FrameDurationMs)
	minFrames := max(1, cfg.MinDurationMs/cfg.FrameDurationMs)
	maxFrames := cfg.MaxDurationMs / cfg.FrameDurationMs

	return &turnBuffer{
		classifier:        classifier,
		sampleRate:        sampleRate,
		channels:          channels,
		frameDurationMs:   cfg.FrameDurationMs,
		frameBytes:        frameBytes,
		frameSamples:      frameSamples,
		silenceSamplesReq: silenceFrames * frameSamples,
		minSamples:        minFrames * frameSamples,
		maxSamples:        maxFrames * frameSamples,
	}, nil
}

func (t *turnBuffer) ingest(data []byte) ([][]byte, error) {
	t.rawData = append(t.rawData, data...)
	t.analysisBuf = append(t.analysisBuf, data...)
	var outputs [][]byte

	for len(t.analysisBuf) >= t.frameBytes {
		frame := make([]byte, t.frameBytes)
		copy(frame, t.analysisBuf[:t.frameBytes])
		t.analysisBuf = t.analysisBuf[t.frameBytes:]
		t.bytesConsumed += t.frameBytes
		t.processedSamples += t.frameSamples

		isSpeech, err := t.classifier.IsSpeech(frame, t.sampleRate, t.channels)
		if err != nil {
			return outputs, err
		}
		if isSpeech {
			t.inSpeech = true
			t.hadSpeech = true
			t.samplesSinceSpeech = 0
		} else if t.inSpeech {
			t.samplesSinceSpeech += t.frameSamples
		}

		shouldFlush := false
		if t.hadSpeech && t.samplesSinceSpeech >= t.silenceSamplesReq && t.processedSamples >= t.minSamples {
			shouldFlush = true
		}
		if t.hadSpeech && t.maxSamples > 0 && t.processedSamples >= t.maxSamples {
			shouldFlush = true
		}
		if shouldFlush {
			if chunk := t.popChunk(false); len(chunk) > 0 {
				outputs = append(outputs, chunk)
			}
		}
	}
	return outputs, nil
}

func (t *turnBuffer) flushForce() [][]byte {
	if len(t.rawData) == 0 || !t.hadSpeech {
		t.resetState()
		return nil
	}
	chunk := t.popChunk(true)
	if len(chunk) == 0 {
		return nil
	}
	return [][]byte{chunk}
}

func (t *turnBuffer) popChunk(includePartial bool) []byte {
	var chunkLen int
	if includePartial {
		chunkLen = len(t.rawData)
	} else {
		chunkLen = t.bytesConsumed
	}
	if chunkLen <= 0 {
		t.resetState()
		return nil
	}
	chunk := make([]byte, chunkLen)
	copy(chunk, t.rawData[:chunkLen])
	if includePartial {
		t.resetState()
	} else {
		t.rawData = t.rawData[chunkLen:]
		t.bytesConsumed = 0
		t.processedSamples = 0
		t.samplesSinceSpeech = 0
		t.inSpeech = false
		t.hadSpeech = false
	}
	return chunk
}

func (t *turnBuffer) resetState() {
	t.rawData = nil
	t.analysisBuf = nil
	t.bytesConsumed = 0
	t.processedSamples = 0
	t.samplesSinceSpeech = 0
	t.inSpeech = false
	t.hadSpeech = false
}

// --- audio helpers and types ---
func pcmStatsInt16(data []byte) (rms float64, peak int) {
	if len(data) < 2 {
		return 0, 0
	}
	sampleCount := len(data) / 2
	var sumSquares float64
	for i := 0; i < sampleCount; i++ {
		v := int(int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2])))
		abs := v
		if abs < 0 {
			abs = -abs
		}
		if abs > peak {
			peak = abs
		}
		sumSquares += float64(v * v)
	}
	rms = math.Sqrt(sumSquares / float64(sampleCount))
	return rms, peak
}

type roomInfo struct {
	Name string `json:"name"`
	SID  string `json:"sid"`
}

type participantInfo struct {
	Identity string `json:"identity"`
	SID      string `json:"sid"`
}

type audioBuffer struct {
	Encoding   string                  `json:"encoding"`
	SampleRate int                     `json:"sampleRate"`
	Channels   int                     `json:"channels"`
	Data       []byte                  `json:"data"`
	Storage    *media.StorageReference `json:"storage,omitempty"`
}

type audioPacket struct {
	Type        string          `json:"type"`
	Room        roomInfo        `json:"room"`
	Participant participantInfo `json:"participant"`
	Audio       audioBuffer     `json:"audio"`
}

type participantContext struct {
	room        roomInfo
	participant participantInfo
}

type turnPacket struct {
	Type        string          `json:"type"`
	Room        roomInfo        `json:"room"`
	Participant participantInfo `json:"participant"`
	Sequence    int             `json:"sequence"`
	DurationMs  int             `json:"durationMs"`
	Timestamp   time.Time       `json:"timestamp"`
	Audio       audioBuffer     `json:"audio"`
}

type turnChunk struct {
	PCM        []byte
	SampleRate int
	Channels   int
}

func isSupportedAudioPacket(packetType string) bool {
	return strings.EqualFold(packetType, audioPacketType)
}

func wrapPCMAsWAV(data []byte, sampleRate, channels int) []byte {
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	if channels <= 0 {
		channels = 1
	}
	blockAlign := channels * bytesPerSample
	byteRate := sampleRate * blockAlign
	chunkSize := 36 + len(data)

	buf := bytes.NewBuffer(make([]byte, 0, chunkSize+8))
	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(chunkSize))
	buf.WriteString("WAVEfmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(data)))
	buf.Write(data)
	return buf.Bytes()
}

func frameSizeBytes(frameDurationMs, sampleRate, channels int) int {
	if frameDurationMs <= 0 || sampleRate <= 0 || channels <= 0 {
		return 0
	}
	samples := sampleRate * frameDurationMs / 1000
	return samples * channels * bytesPerSample
}

func calcDurationMs(bytesLen, sampleRate, channels int) int {
	if sampleRate <= 0 || channels <= 0 {
		return 0
	}
	samples := bytesLen / (channels * bytesPerSample)
	if samples == 0 {
		return 0
	}
	return samples * 1000 / sampleRate
}

func isHeartbeat(meta map[string]string) bool {
	if meta == nil {
		return false
	}
	return meta["bubu-heartbeat"] == "true"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func downmixToMono(frame []byte, channels int) ([]int16, error) {
	if channels <= 0 {
		return nil, fmt.Errorf("invalid channel count %d", channels)
	}
	samples := len(frame) / (bytesPerSample * channels)
	out := make([]int16, samples)
	for i := 0; i < samples; i++ {
		var sum int32
		for ch := 0; ch < channels; ch++ {
			idx := (i*channels + ch) * bytesPerSample
			sum += int32(int16(binary.LittleEndian.Uint16(frame[idx : idx+2])))
		}
		out[i] = int16(sum / int32(channels))
	}
	return out, nil
}

func resampleInt16Mono(samples []int16, fromRate, toRate int) ([]int16, error) {
	if fromRate <= 0 || toRate <= 0 {
		return nil, fmt.Errorf("invalid sample rates")
	}
	if len(samples) == 0 {
		return []int16{}, nil
	}
	if fromRate == toRate {
		out := make([]int16, len(samples))
		copy(out, samples)
		return out, nil
	}
	// For integer downsampling ratios, use FIR-filtered decimation to avoid
	// aliasing.  The previous linear-interpolation code degenerated to pure
	// sample-picking for integer ratios (e.g. 48 kHz → 16 kHz), which folds
	// all content above the target Nyquist back into the baseband, corrupting
	// speech and making the Silero VAD unable to detect it.
	if fromRate > toRate && fromRate%toRate == 0 {
		return decimateInt16(samples, fromRate/toRate, float64(toRate)/2, float64(fromRate)), nil
	}
	// Non-integer ratios: linear interpolation (acceptable when the ratio is
	// not integer because fractional positions provide implicit filtering).
	ratio := float64(toRate) / float64(fromRate)
	outSamples := int(math.Ceil(float64(len(samples)-1)*ratio)) + 1
	if outSamples <= 0 {
		outSamples = 1
	}
	out := make([]int16, outSamples)
	for i := 0; i < outSamples; i++ {
		srcPos := float64(i) / ratio
		idx := int(srcPos)
		if idx >= len(samples)-1 {
			out[i] = samples[len(samples)-1]
			continue
		}
		frac := srcPos - float64(idx)
		a := float64(samples[idx])
		b := float64(samples[idx+1])
		sample := a + frac*(b-a)
		out[i] = int16(sample)
	}
	return out, nil
}

// decimateInt16 downsamples int16 mono audio by an integer factor, applying a
// windowed-sinc FIR low-pass filter to prevent aliasing before decimation.
func decimateInt16(samples []int16, factor int, cutoffHz, sampleRate float64) []int16 {
	if factor <= 1 || len(samples) == 0 {
		return append([]int16(nil), samples...)
	}
	kernel := buildLowPassKernel(cutoffHz, sampleRate, factor*8+1)
	half := len(kernel) / 2
	n := len(samples)
	out := make([]int16, 0, n/factor+1)
	for i := 0; i < n; i += factor {
		var sum float64
		for k, coeff := range kernel {
			j := i + k - half
			if j < 0 {
				j = 0
			} else if j >= n {
				j = n - 1
			}
			sum += float64(samples[j]) * coeff
		}
		v := int64(math.Round(sum))
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		out = append(out, int16(v))
	}
	return out
}

// buildLowPassKernel returns a normalised windowed-sinc FIR low-pass filter.
// cutoffHz is the –6 dB frequency; sampleRate is the input rate; taps must be odd.
func buildLowPassKernel(cutoffHz, sampleRate float64, taps int) []float64 {
	if taps%2 == 0 {
		taps++
	}
	kernel := make([]float64, taps)
	m := taps / 2
	fc := cutoffHz / sampleRate // normalised cutoff (0..0.5)
	var sum float64
	for i := 0; i < taps; i++ {
		n := float64(i - m)
		if n == 0 {
			kernel[i] = 2 * math.Pi * fc
		} else {
			kernel[i] = math.Sin(2*math.Pi*fc*n) / n
		}
		// Blackman window for ~58 dB stopband attenuation.
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(taps-1)) +
			0.08*math.Cos(4*math.Pi*float64(i)/float64(taps-1))
		kernel[i] *= w
		sum += kernel[i]
	}
	for i := range kernel {
		kernel[i] /= sum
	}
	return kernel
}

func bytesToInt16LE(pcm []byte) ([]int16, error) {
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("pcm byte length must be even, got %d", len(pcm))
	}
	out := make([]int16, len(pcm)/2)
	for i := 0; i < len(out); i++ {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return out, nil
}

func int16ToBytesLE(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func int16ToFloat32(samples []int16) []float32 {
	out := make([]float32, len(samples))
	const denom = 32768.0
	for i, sample := range samples {
		out[i] = float32(float64(sample) / denom)
	}
	return out
}

func applyAGC(samples []int16, debugCount int) []int16 {
	if len(samples) == 0 {
		return samples
	}
	// Target RMS for Silero input: 3000/32768 ≈ 0.09 float32, which is
	// within Silero's typical speech range.  maxGain is set high because
	// LiveKit browser captures often produce int16 RMS of only 5–150
	// (microphone gain varies wildly); at maxGain=12 the signal stays
	// below Silero's detection floor even with real speech.
	const targetRMS = 3000.0
	const maxGain = 200.0
	rms := rmsInt16(samples)
	if rms <= 1 {
		return samples
	}
	gain := targetRMS / rms
	if gain > maxGain {
		gain = maxGain
	}
	if gain < 1.0 {
		return samples
	}
	out := make([]int16, len(samples))
	for i, v := range samples {
		scaled := float64(v) * gain
		if scaled > math.MaxInt16 {
			scaled = math.MaxInt16
		} else if scaled < math.MinInt16 {
			scaled = math.MinInt16
		}
		out[i] = int16(scaled)
	}
	if debugCount < 200 {
		slog.Info("silero agc applied", "rms", fmt.Sprintf("%.2f", rms), "gain", fmt.Sprintf("%.2f", gain))
	}
	return out
}

func rmsInt16(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sumSquares float64
	for _, v := range samples {
		sumSquares += float64(v) * float64(v)
	}
	return math.Sqrt(sumSquares / float64(len(samples)))
}
