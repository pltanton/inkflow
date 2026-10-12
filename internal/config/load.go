package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
	if cfg.DefaultPDFDir == "" {
		cfg.DefaultPDFDir = "Attachments/Boox"
	}
	if cfg.DefaultNoteDir == "" {
		cfg.DefaultNoteDir = "00 Inbox"
	}
	if cfg.Gemini.Model == "" {
		cfg.Gemini.Model = "gemini-3.5-flash"
	}
	if cfg.Gemini.Timeout == "" {
		cfg.Gemini.Timeout = "60s"
	}
	if cfg.Gemini.OCRPrompt == "" {
		cfg.Gemini.OCRPrompt = "Transcribe the handwritten page as clean readable Markdown. The goal is a document that reads well, not a pixel-accurate copy of paper layout. " +
			"Join visually wrapped lines that belong to one sentence into a single flowing line. Do not preserve every line break from the paper. " +
			"When the writer puts a single name or short phrase above a related cluster of items (e.g. a person owning a list of bullets), render that header as a Markdown heading: `### Name`. " +
			"Render dash, bullet, or arrow markers on the page as `-` list items. " +
			"Use a blank line only between structural sections, not after every visual line wrap. " +
			"Preserve visual markup: wrap text highlighted with a marker pen in `==text==`; wrap text inside a hand-drawn frame or box in `**text**` as a single bold span even if it wrapped across multiple lines; render hand-drawn checkboxes as `- [ ]` (empty) or `- [x]` (ticked). " +
			"Keep the source language. Faithful transcription only — no translation, no summarization."
	}
	if cfg.Gemini.SummaryPrompt == "" {
		cfg.Gemini.SummaryPrompt = "Summarize as 3-5 short bullets covering action items, decisions, deadlines, people. Use the source language. " +
			"Plain bullets only — do not produce `[ ]` or `[x]` checkboxes. The reader maintains a separate TODO section elsewhere in the note."
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
	if cfg.Upstream.Prefix != "" {
		p := cfg.Upstream.Prefix
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		p = strings.TrimSuffix(p, "/")
		cfg.Upstream.Prefix = p
	}
}

func validate(cfg *Config) error {
	if cfg.VaultDir == "" {
		return fmt.Errorf("vault_dir is required")
	}
	for _, r := range cfg.Routes {
		if r.From == "" {
			return fmt.Errorf("route.from is required")
		}
	}
	d, err := time.ParseDuration(cfg.Gemini.Retry.Backoff)
	if err != nil {
		return fmt.Errorf("gemini.retry.backoff %q: %w", cfg.Gemini.Retry.Backoff, err)
	}
	if d <= 0 {
		return fmt.Errorf("gemini.retry.backoff must be a positive duration, got %q", cfg.Gemini.Retry.Backoff)
	}
	cfg.Gemini.Retry.BackoffDuration = d
	if cfg.Gemini.Retry.Enabled && cfg.Gemini.Retry.MaxRetries < 1 {
		return fmt.Errorf("gemini.retry.max_retries must be >= 1 when retry is enabled, got %d", cfg.Gemini.Retry.MaxRetries)
	}
	if cfg.Upstream.URL != "" {
		if cfg.Upstream.User == "" {
			return fmt.Errorf("upstream.user is required when upstream.url is set")
		}
		if cfg.Upstream.PasswordFile == "" {
			return fmt.Errorf("upstream.password_file is required when upstream.url is set")
		}
	}
	return nil
}
