package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"inkflow/internal/config"
)

func TestResolveAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "from-env")
	got, err := resolveAPIKey(config.GeminiConfig{APIKeyFile: "/does/not/exist"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAPIKeyFallsBackToFile(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAPIKey(config.GeminiConfig{APIKeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-file" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAPIKeyMissingErrors(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	_, err := resolveAPIKey(config.GeminiConfig{})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestAnyRouteWantsAIDetectsFlag(t *testing.T) {
	if anyRouteWantsAI(nil) {
		t.Error("nil routes should not want AI")
	}
	if !anyRouteWantsAI([]config.Route{{AI: false}, {AI: true}}) {
		t.Error("one AI route should enable provider")
	}
	if anyRouteWantsAI([]config.Route{{AI: false}, {AI: false}}) {
		t.Error("no AI routes should not enable provider")
	}
}

func TestResolveCalDAVPassword(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pw")
	if err := os.WriteFile(file, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		env     string
		file    string
		want    string
		wantErr bool
	}{
		{name: "env wins", env: "from-env", file: file, want: "from-env"},
		{name: "file fallback", file: file, want: "from-file"},
		{name: "missing", wantErr: true},
		{name: "unreadable file", file: filepath.Join(dir, "absent"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("INKFLOW_CALDAV_PASSWORD", tt.env)
			got, err := resolveCalDAVPassword(config.CalDAVConfig{PasswordFile: tt.file})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q", got)
			}
		})
	}
}
