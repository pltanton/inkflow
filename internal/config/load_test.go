package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadParsesGeminiConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini]
api_key_file = "/run/secrets/g"
model = "gemini-3.5-flash"
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
	if cfg.Gemini.Model != "gemini-3.5-flash" {
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
	if cfg.Gemini.Model != "gemini-3.5-flash" {
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
	if !strings.Contains(cfg.Gemini.TasksPrompt, "my_tasks") {
		t.Errorf("default tasks_prompt does not mention my_tasks: %q", cfg.Gemini.TasksPrompt)
	}
	if cfg.CalDAV.Enabled {
		t.Error("expected caldav disabled by default")
	}
	if cfg.CalDAV.ObsidianVault != "vault" {
		t.Errorf("default obsidian_vault = %q, want vault_dir basename", cfg.CalDAV.ObsidianVault)
	}
	if cfg.Routes[0].AI {
		t.Error("expected route.AI default false")
	}
}

func TestLoadParsesRetryConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini.retry]
enabled = true
max_retries = 5
backoff = "1m"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Gemini.Retry.Enabled {
		t.Error("Retry.Enabled = false, want true")
	}
	if cfg.Gemini.Retry.MaxRetries != 5 {
		t.Errorf("Retry.MaxRetries = %d, want 5", cfg.Gemini.Retry.MaxRetries)
	}
	if cfg.Gemini.Retry.Backoff != "1m" {
		t.Errorf("Retry.Backoff = %q, want \"1m\"", cfg.Gemini.Retry.Backoff)
	}
	if cfg.Gemini.Retry.BackoffDuration != time.Minute {
		t.Errorf("Retry.BackoffDuration = %v, want 1m", cfg.Gemini.Retry.BackoffDuration)
	}
}

func TestLoadAppliesRetryDefaults(t *testing.T) {
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
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Gemini.Retry.Enabled {
		t.Error("Retry.Enabled = true, want false")
	}
	if cfg.Gemini.Retry.MaxRetries != 3 {
		t.Errorf("Retry.MaxRetries = %d, want 3", cfg.Gemini.Retry.MaxRetries)
	}
	if cfg.Gemini.Retry.BackoffDuration != 30*time.Second {
		t.Errorf("Retry.BackoffDuration = %v, want 30s", cfg.Gemini.Retry.BackoffDuration)
	}
}

func TestValidateRetryBackoffRejectedWhenUnparseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini.retry]
backoff = "banana"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unparseable backoff, got nil")
	}
	if !strings.Contains(err.Error(), "backoff") {
		t.Errorf("error does not mention backoff: %v", err)
	}
}

func TestValidateRetryBackoffRejectedWhenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini.retry]
backoff = "0s"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for zero backoff, got nil")
	}
}

func TestValidateMaxRetriesRejectedWhenEnabledAndZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini.retry]
enabled = true
max_retries = 0
backoff = "30s"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for max_retries = 0 with enabled = true, got nil")
	}
	if !strings.Contains(err.Error(), "max_retries") {
		t.Errorf("error does not mention max_retries: %v", err)
	}
}

func TestValidateMaxRetriesAcceptedWhenDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[gemini.retry]
enabled = false
max_retries = 0
backoff = "30s"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err != nil {
		t.Fatalf("expected no error for max_retries = 0 with enabled = false, got: %v", err)
	}
}

func TestValidateUpstreamRequiresUserAndPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[upstream]
url = "https://nextcloud.example"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("expected error for upstream.url without user/password_file, got nil")
	}
}

func TestLoadNormalizesUpstreamPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[upstream]
url = "https://nextcloud.example"
prefix = "remote.php/dav/files/anton/"
user = "anton"
password_file = "/run/secrets/x"

[[route]]
from = "Syncs/"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Upstream.Prefix != "/remote.php/dav/files/anton" {
		t.Errorf("Prefix = %q, want leading slash and no trailing slash", cfg.Upstream.Prefix)
	}
}

func TestLoadParsesCalDAVConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inkflow.toml")
	body := `
vault_dir = "/tmp/vault"

[caldav]
enabled = true
url = "https://nextcloud.example/remote.php/dav/calendars/anton/inbox/"
user = "anton"
password_file = "/var/lib/inkflow/caldav-password"
obsidian_vault = "Anton"
categories = ["wallet"]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := cfg.CalDAV
	if !c.Enabled || c.User != "anton" || c.PasswordFile != "/var/lib/inkflow/caldav-password" ||
		c.ObsidianVault != "Anton" || len(c.Categories) != 1 || c.Categories[0] != "wallet" ||
		!strings.HasSuffix(c.URL, "/calendars/anton/inbox/") {
		t.Fatalf("unexpected caldav config: %+v", c)
	}
}

func TestValidateCalDAVRequiresURLAndUserWhenEnabled(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing url", body: "[caldav]\nenabled = true\nuser = \"anton\"\n"},
		{name: "missing user", body: "[caldav]\nenabled = true\nurl = \"https://x\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "inkflow.toml")
			if err := os.WriteFile(path, []byte("vault_dir = \"/tmp/vault\"\n"+tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "caldav") {
				t.Fatalf("expected caldav validation error, got %v", err)
			}
		})
	}
}
