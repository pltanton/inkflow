package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultOCRPrompt = "Transcribe the handwritten page as clean readable Markdown. The goal is a document that reads well, not a pixel-accurate copy of paper layout. " +
		"Join visually wrapped lines that belong to one sentence into a single flowing line. Do not preserve every line break from the paper. " +
		"When the writer puts a single name or short phrase above a related cluster of items (e.g. a person owning a list of bullets), render that header as a Markdown heading: `### Name`. " +
		"Render dash, bullet, or arrow markers on the page as `-` list items. " +
		"Use a blank line only between structural sections, not after every visual line wrap. " +
		"Preserve visual markup: wrap text highlighted with a marker pen in `==text==`; wrap text inside a hand-drawn frame or box in `**text**` as a single bold span even if it wrapped across multiple lines; render hand-drawn checkboxes as `- [ ]` (empty) or `- [x]` (ticked). " +
		"Keep the source language. Faithful transcription only — no translation, no summarization."
	defaultSummaryPrompt = "Summarize as 3-5 short bullets covering action items, decisions, deadlines, people. Use the source language. " +
		"Plain bullets only — do not produce `[ ]` or `[x]` checkboxes. The reader maintains a separate TODO section elsewhere in the note."
)

func Load(path string) (*Config, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", err
	}
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return nil, "", err
	}
	applyDefaults(&cfg, md)
	if err := validate(&cfg); err != nil {
		return nil, "", err
	}
	return &cfg, filepath.Dir(abs), nil
}

func applyDefaults(cfg *Config, md toml.MetaData) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.ReadHeaderTimeout == "" {
		cfg.ReadHeaderTimeout = "5s"
	}
	if cfg.ReadTimeout == "" {
		cfg.ReadTimeout = "2m"
	}
	if cfg.WriteTimeout == "" {
		cfg.WriteTimeout = "2m"
	}
	if cfg.IdleTimeout == "" {
		cfg.IdleTimeout = "1m"
	}
	if !md.IsDefined("max_upload_bytes") {
		cfg.MaxUploadBytes = 100 * 1024 * 1024
	}
	if cfg.DefaultPDFDir == "" {
		cfg.DefaultPDFDir = "Attachments/Boox"
	}
	if cfg.DefaultNoteDir == "" {
		cfg.DefaultNoteDir = "00 Inbox"
	}
	if cfg.AI.Provider == "" {
		cfg.AI.Provider = "gemini"
	}
	if cfg.Gemini.Model == "" {
		cfg.Gemini.Model = "gemini-3.5-flash"
	}
	if cfg.Gemini.Timeout == "" {
		cfg.Gemini.Timeout = "60s"
	}
	if cfg.Gemini.MinReprocessInterval == "" {
		cfg.Gemini.MinReprocessInterval = "0s"
	}
	if cfg.Gemini.OCRPrompt == "" {
		cfg.Gemini.OCRPrompt = defaultOCRPrompt
	}
	if cfg.Gemini.SummaryPrompt == "" {
		cfg.Gemini.SummaryPrompt = defaultSummaryPrompt
	}
	if cfg.OpenAI.Model == "" {
		cfg.OpenAI.Model = "gpt-4.1"
	}
	if cfg.OpenAI.Timeout == "" {
		cfg.OpenAI.Timeout = "60s"
	}
	if cfg.OpenAI.OCRPrompt == "" {
		cfg.OpenAI.OCRPrompt = defaultOCRPrompt
	}
	if cfg.OpenAI.SummaryPrompt == "" {
		cfg.OpenAI.SummaryPrompt = defaultSummaryPrompt
	}
	if cfg.Ollama.BaseURL == "" {
		cfg.Ollama.BaseURL = "http://localhost:11434"
	}
	if cfg.Ollama.Timeout == "" {
		cfg.Ollama.Timeout = "60s"
	}
	if cfg.Ollama.OCRPrompt == "" {
		cfg.Ollama.OCRPrompt = defaultOCRPrompt
	}
	if cfg.Ollama.SummaryPrompt == "" {
		cfg.Ollama.SummaryPrompt = defaultSummaryPrompt
	}
	for index := range cfg.Routes {
		if cfg.Routes[index].TagMergeStrategy == "" {
			cfg.Routes[index].TagMergeStrategy = "merge"
		}
	}
	// Retry defaults: only apply MaxRetries when the key was not explicitly
	// set in the TOML source, so that max_retries = 0 is preserved for
	// validation (e.g. rejected when enabled = true).
	if !md.IsDefined("gemini", "retry", "max_retries") {
		cfg.Gemini.Retry.MaxRetries = 3
	}
	if cfg.Gemini.Retry.Backoff == "" {
		cfg.Gemini.Retry.Backoff = "30s"
	}
	// Enabled defaults to false (Go zero value); no explicit assignment needed.
}

func validate(cfg *Config) error {
	if cfg.VaultDir == "" {
		return fmt.Errorf("vault_dir is required")
	}
	routeIndexes := make(map[string]int, len(cfg.Routes))
	for i, r := range cfg.Routes {
		if r.From == "" {
			return fmt.Errorf("route %d from is required", i+1)
		}
		if r.TagMergeStrategy != "merge" && r.TagMergeStrategy != "replace" {
			return fmt.Errorf("route.tag_merge_strategy must be \"merge\" or \"replace\", got %q", r.TagMergeStrategy)
		}
		normalized := NormalizeRoutePrefix(r.From)
		if previous, ok := routeIndexes[normalized]; ok {
			return fmt.Errorf("route %d from %q conflicts with route %d from %q after normalization to %q", i+1, r.From, previous+1, cfg.Routes[previous].From, normalized)
		}
		routeIndexes[normalized] = i
	}
	d, err := time.ParseDuration(cfg.Gemini.Retry.Backoff)
	if err != nil {
		return fmt.Errorf("gemini.retry.backoff %q: %w", cfg.Gemini.Retry.Backoff, err)
	}
	if d <= 0 {
		return fmt.Errorf("gemini.retry.backoff must be a positive duration, got %q", cfg.Gemini.Retry.Backoff)
	}
	cfg.Gemini.Retry.BackoffDuration = d
	minReprocessInterval, err := time.ParseDuration(cfg.Gemini.MinReprocessInterval)
	if err != nil {
		return fmt.Errorf("gemini.min_reprocess_interval %q: must be a non-negative duration: %w", cfg.Gemini.MinReprocessInterval, err)
	}
	if minReprocessInterval < 0 {
		return fmt.Errorf("gemini.min_reprocess_interval must be a non-negative duration, got %q", cfg.Gemini.MinReprocessInterval)
	}
	cfg.Gemini.MinReprocessIntervalDuration = minReprocessInterval
	if cfg.Gemini.Retry.Enabled && cfg.Gemini.Retry.MaxRetries < 1 {
		return fmt.Errorf("gemini.retry.max_retries must be >= 1 when retry is enabled, got %d", cfg.Gemini.Retry.MaxRetries)
	}
	if cfg.AI.Provider != "gemini" && cfg.AI.Provider != "openai" && cfg.AI.Provider != "ollama" {
		return fmt.Errorf("ai.provider must be \"gemini\", \"openai\", or \"ollama\", got %q", cfg.AI.Provider)
	}
	if cfg.AI.Provider == "ollama" && cfg.Ollama.Model == "" {
		return fmt.Errorf("ollama.model is required when ai.provider is \"ollama\"")
	}
	for _, timeout := range []struct {
		name  string
		value string
		out   *time.Duration
	}{
		{"read_header_timeout", cfg.ReadHeaderTimeout, &cfg.ReadHeaderTimeoutDuration},
		{"read_timeout", cfg.ReadTimeout, &cfg.ReadTimeoutDuration},
		{"write_timeout", cfg.WriteTimeout, &cfg.WriteTimeoutDuration},
		{"idle_timeout", cfg.IdleTimeout, &cfg.IdleTimeoutDuration},
	} {
		duration, err := time.ParseDuration(timeout.value)
		if err != nil {
			return fmt.Errorf("%s %q: %w", timeout.name, timeout.value, err)
		}
		if duration <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %q", timeout.name, timeout.value)
		}
		*timeout.out = duration
	}
	if cfg.MaxUploadBytes <= 0 {
		return fmt.Errorf("max_upload_bytes must be positive, got %d", cfg.MaxUploadBytes)
	}
	if cfg.Observability.MetricsAddr != "" {
		if _, _, err := net.SplitHostPort(cfg.Observability.MetricsAddr); err != nil {
			return fmt.Errorf("observability.metrics_addr %q: %w", cfg.Observability.MetricsAddr, err)
		}
	}
	return nil
}
