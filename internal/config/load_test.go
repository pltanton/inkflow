package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesGeminiConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini]
api_key_file = "/run/secrets/g"
model = "gemini-2.5-flash"
timeout = "30s"
ocr_prompt = "ocr please"
summary_prompt = "summary please"

[[route]]
from = "Syncs/"
ai = true
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gemini.APIKeyFile != "/run/secrets/g" {
		t.Errorf("APIKeyFile = %q", cfg.Gemini.APIKeyFile)
	}
	if cfg.Gemini.Model != "gemini-2.5-flash" {
		t.Errorf("Model = %q", cfg.Gemini.Model)
	}
	if cfg.Gemini.Timeout != "30s" {
		t.Errorf("Timeout = %q", cfg.Gemini.Timeout)
	}
	if cfg.Gemini.OCRPrompt != "ocr please" {
		t.Errorf("OCRPrompt = %q", cfg.Gemini.OCRPrompt)
	}
	if cfg.Gemini.SummaryPrompt != "summary please" {
		t.Errorf("SummaryPrompt = %q", cfg.Gemini.SummaryPrompt)
	}
	if len(cfg.Routes) == 0 || !cfg.Routes[0].AI {
		t.Fatalf("expected route.AI=true; got %+v", cfg.Routes)
	}
}

func TestLoadAppliesGeminiDefaultsWhenBlockOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gemini.Model != "gemini-2.5-flash" {
		t.Errorf("default model = %q", cfg.Gemini.Model)
	}
	if cfg.Gemini.Timeout != "60s" {
		t.Errorf("default timeout = %q", cfg.Gemini.Timeout)
	}
	if cfg.Gemini.OCRPrompt == "" {
		t.Error("default ocr_prompt is empty")
	}
	if cfg.Gemini.SummaryPrompt == "" {
		t.Error("default summary_prompt is empty")
	}
	if cfg.Routes[0].AI {
		t.Error("expected route.AI default false")
	}
}
