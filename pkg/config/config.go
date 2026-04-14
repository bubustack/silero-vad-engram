package config

import "strings"

const (
	outputModePacket  = "packet"
	outputModeAudio   = "audio"
	outputEncodingWAV = "wav"
)

// Config captures static silero-vad settings injected via Engram.spec.with.
type Config struct {
	ModelPath             string  `json:"modelPath" mapstructure:"modelPath"`
	DetectorSampleRate    int     `json:"detectorSampleRateHz" mapstructure:"detectorSampleRateHz"`
	OutputSampleRateHz    int     `json:"outputSampleRateHz" mapstructure:"outputSampleRateHz"`
	OutputMode            string  `json:"outputMode" mapstructure:"outputMode"`
	Threshold             float32 `json:"threshold" mapstructure:"threshold"`
	MinSilenceDuration    int     `json:"minSilenceDurationMs" mapstructure:"minSilenceDurationMs"`
	MinDurationMs         int     `json:"minDurationMs" mapstructure:"minDurationMs"`
	MaxDurationMs         int     `json:"maxDurationMs" mapstructure:"maxDurationMs"`
	FrameDurationMs       int     `json:"frameDurationMs" mapstructure:"frameDurationMs"`
	OutputEncoding        string  `json:"outputEncoding" mapstructure:"outputEncoding"`
	InlineAudioLimit      int     `json:"inlineAudioLimit" mapstructure:"inlineAudioLimit"`
	CoalesceDurationMs    int     `json:"coalesceDurationMs" mapstructure:"coalesceDurationMs"`
	CoalesceMaxDurationMs int     `json:"coalesceMaxDurationMs" mapstructure:"coalesceMaxDurationMs"`
}

// Normalize clamps and default-fills configuration values.
func Normalize(cfg Config) Config {
	cfg = normalizeModelConfig(cfg)
	cfg = normalizeOutputConfig(cfg)
	cfg = normalizeThresholdConfig(cfg)
	cfg = normalizeDurationConfig(cfg)
	return normalizeCoalesceConfig(cfg)
}

func normalizeModelConfig(cfg Config) Config {
	if strings.TrimSpace(cfg.ModelPath) == "" {
		cfg.ModelPath = "/models/silero_vad.onnx"
	}
	if cfg.DetectorSampleRate != 8000 && cfg.DetectorSampleRate != 16000 {
		cfg.DetectorSampleRate = 16000
	}
	return cfg
}

func normalizeOutputConfig(cfg Config) Config {
	if cfg.OutputSampleRateHz < 0 {
		cfg.OutputSampleRateHz = 0
	}
	cfg.OutputMode = strings.TrimSpace(strings.ToLower(cfg.OutputMode))
	switch cfg.OutputMode {
	case "", outputModePacket:
		cfg.OutputMode = outputModePacket
	case outputModeAudio:
		cfg.OutputMode = outputModeAudio
	default:
		cfg.OutputMode = outputModePacket
	}
	if strings.TrimSpace(cfg.OutputEncoding) == "" {
		cfg.OutputEncoding = outputEncodingWAV
	}
	return cfg
}

func normalizeThresholdConfig(cfg Config) Config {
	if cfg.Threshold <= 0 || cfg.Threshold >= 1 {
		cfg.Threshold = 0.5
	}
	return cfg
}

func normalizeDurationConfig(cfg Config) Config {
	if cfg.MinSilenceDuration <= 0 {
		cfg.MinSilenceDuration = 400
	}
	if cfg.MinDurationMs <= 0 {
		cfg.MinDurationMs = 200
	}
	if cfg.MaxDurationMs <= 0 {
		cfg.MaxDurationMs = 12000
	}
	if cfg.FrameDurationMs <= 0 {
		cfg.FrameDurationMs = 32
	}
	return cfg
}

func normalizeCoalesceConfig(cfg Config) Config {
	if cfg.CoalesceDurationMs < 0 {
		cfg.CoalesceDurationMs = 0
	}
	if cfg.CoalesceDurationMs > 0 && cfg.CoalesceMaxDurationMs <= 0 {
		cfg.CoalesceMaxDurationMs = 30000
	}
	return cfg
}
