package config

import "strings"

// Config captures static silero-vad settings injected via Engram.spec.with.
type Config struct {
	ModelPath          string  `json:"modelPath" mapstructure:"modelPath"`
	DetectorSampleRate int     `json:"detectorSampleRateHz" mapstructure:"detectorSampleRateHz"`
	OutputSampleRateHz int     `json:"outputSampleRateHz" mapstructure:"outputSampleRateHz"`
	OutputMode         string  `json:"outputMode" mapstructure:"outputMode"`
	Threshold          float32 `json:"threshold" mapstructure:"threshold"`
	MinSilenceDuration int     `json:"minSilenceDurationMs" mapstructure:"minSilenceDurationMs"`
	MinDurationMs      int     `json:"minDurationMs" mapstructure:"minDurationMs"`
	MaxDurationMs      int     `json:"maxDurationMs" mapstructure:"maxDurationMs"`
	FrameDurationMs    int     `json:"frameDurationMs" mapstructure:"frameDurationMs"`
	OutputEncoding     string  `json:"outputEncoding" mapstructure:"outputEncoding"`
	InlineAudioLimit      int     `json:"inlineAudioLimit" mapstructure:"inlineAudioLimit"`
	CoalesceDurationMs    int     `json:"coalesceDurationMs" mapstructure:"coalesceDurationMs"`
	CoalesceMaxDurationMs int     `json:"coalesceMaxDurationMs" mapstructure:"coalesceMaxDurationMs"`
}

// Normalize clamps and default-fills configuration values.
func Normalize(cfg Config) Config {
	if strings.TrimSpace(cfg.ModelPath) == "" {
		cfg.ModelPath = "/models/silero_vad.onnx"
	}
	if cfg.DetectorSampleRate != 8000 && cfg.DetectorSampleRate != 16000 {
		cfg.DetectorSampleRate = 16000
	}
	if cfg.OutputSampleRateHz < 0 {
		cfg.OutputSampleRateHz = 0
	}
	if strings.TrimSpace(cfg.OutputMode) == "" {
		cfg.OutputMode = "packet"
	}
	if cfg.OutputMode != "packet" && cfg.OutputMode != "audio" {
		cfg.OutputMode = "packet"
	}
	if cfg.Threshold <= 0 || cfg.Threshold >= 1 {
		cfg.Threshold = 0.5
	}
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
	if strings.TrimSpace(cfg.OutputEncoding) == "" {
		cfg.OutputEncoding = "wav"
	}
	if cfg.CoalesceDurationMs < 0 {
		cfg.CoalesceDurationMs = 0
	}
	if cfg.CoalesceDurationMs > 0 && cfg.CoalesceMaxDurationMs <= 0 {
		cfg.CoalesceMaxDurationMs = 30000
	}
	return cfg
}
