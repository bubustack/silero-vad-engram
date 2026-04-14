package engram

import (
	"context"
	"log/slog"

	sdk "github.com/bubustack/bubu-sdk-go"
)

func (e *SileroVAD) debugEnabled(ctx context.Context, logger *slog.Logger) bool {
	if sdk.DebugModeEnabled() {
		return true
	}
	if logger == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return logger.Enabled(ctx, slog.LevelDebug)
}

func (e *SileroVAD) logDetectorInput(ctx context.Context, logger *slog.Logger, pkt *audioPacket) {
	if !e.debugEnabled(ctx, logger) || pkt == nil {
		return
	}
	logger.Debug("silero-vad received audio",
		slog.String("participant", pkt.Participant.Identity),
		slog.String("room", pkt.Room.Name),
		slog.Int("bytes", len(pkt.Audio.Data)),
		slog.Int("sampleRate", pkt.Audio.SampleRate),
		slog.Int("channels", pkt.Audio.Channels),
		slog.String("encoding", pkt.Audio.Encoding),
	)
}

func (e *SileroVAD) logDetectorOutput(ctx context.Context, logger *slog.Logger, pkt *turnPacket) {
	if !e.debugEnabled(ctx, logger) || pkt == nil {
		return
	}
	payloadBytes := 0
	if pkt.Audio.Data != nil {
		payloadBytes = len(pkt.Audio.Data)
	}
	logger.Debug("silero-vad emitted chunk",
		slog.String("participant", pkt.Participant.Identity),
		slog.Int("sequence", pkt.Sequence),
		slog.Int("durationMs", pkt.DurationMs),
		slog.Int("bytes", payloadBytes),
		slog.String("encoding", pkt.Audio.Encoding),
	)
}

func (e *SileroVAD) logIgnoredPacket(ctx context.Context, logger *slog.Logger, pkt *audioPacket, reason string) {
	if !e.debugEnabled(ctx, logger) || pkt == nil {
		return
	}
	logger.Debug("silero-vad ignored packet",
		slog.String("participant", pkt.Participant.Identity),
		slog.String("room", pkt.Room.Name),
		slog.String("reason", reason),
		slog.String("type", pkt.Type),
	)
}

func (e *SileroVAD) logStorageDecision(
	ctx context.Context,
	logger *slog.Logger,
	participant string,
	offloaded bool,
	bytes int,
	err error,
) {
	if !e.debugEnabled(ctx, logger) {
		return
	}
	attrs := []any{
		slog.String("participant", participant),
		slog.Int("bytes", bytes),
		slog.Bool("offloaded", offloaded),
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
	}
	logger.Debug("silero-vad storage decision", attrs...)
}
