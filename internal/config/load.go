package config

import (
	"fmt"
	"os"
	"path/filepath"

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
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, "", err
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, "", err
	}
	return &cfg, filepath.Dir(abs), nil
}

func applyDefaults(cfg *Config) {
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
		cfg.Gemini.Model = "gemini-2.5-flash"
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
	return nil
}
