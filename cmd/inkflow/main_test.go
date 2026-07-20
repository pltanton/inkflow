package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"inkflow/internal/config"
)

func TestResolveAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "from-env")
	got, err := resolveGeminiAPIKey(config.GeminiConfig{APIKeyFile: "/does/not/exist"})
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
	got, err := resolveGeminiAPIKey(config.GeminiConfig{APIKeyFile: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-file" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAPIKeyMissingErrors(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	_, err := resolveGeminiAPIKey(config.GeminiConfig{})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestLoadRuntimeOpenAIWithEnvKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "from-env")
	t.Setenv("GEMINI_API_KEY", "")
	rt := loadOpenAIRuntime(t, "")
	if rt.cfg.AI.Provider != "openai" {
		t.Errorf("provider = %q, want openai", rt.cfg.AI.Provider)
	}
}

func TestLoadRuntimeOpenAIMissingKeyErrors(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	dir := t.TempDir()
	configPath := writeRuntimeConfig(t, dir, true, "")
	_, err := loadRuntime(slog.Default(), configPath)
	if err == nil || !strings.Contains(err.Error(), "openai: no API key — set $OPENAI_API_KEY or [openai].api_key_file") {
		t.Fatalf("expected OpenAI missing-key error, got %v", err)
	}
}

func TestLoadRuntimeOpenAIWithKeyFile(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "openai-key")
	if err := os.WriteFile(keyPath, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadOpenAIRuntime(t, keyPath)
}

func TestLoadRuntimeOpenAIWithoutAIRouteSkipsKeyResolution(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	dir := t.TempDir()
	configPath := writeRuntimeConfig(t, dir, false, "")
	rt, err := loadRuntime(slog.Default(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })
}

func TestLoadRuntimeOllamaWithoutCredentials(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "inkflow.toml")
	contents := "vault_dir = \"" + filepath.Join(dir, "vault") + "\"\n" +
		"state_file = \"state.db\"\n\n" +
		"[ai]\nprovider = \"ollama\"\n\n" +
		"[ollama]\nmodel = \"llama3.2-vision\"\n\n" +
		"[[route]]\nfrom = \"/uploads\"\nai = true\n"
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := loadRuntime(slog.Default(), configPath)
	if err != nil {
		t.Fatalf("Ollama should not require cloud credentials: %v", err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })
}

func loadOpenAIRuntime(t *testing.T, keyPath string) runtime {
	t.Helper()
	dir := t.TempDir()
	configPath := writeRuntimeConfig(t, dir, true, keyPath)
	rt, err := loadRuntime(slog.Default(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.store.Close() })
	return rt
}

func writeRuntimeConfig(t *testing.T, dir string, wantsAI bool, keyPath string) string {
	t.Helper()
	configPath := filepath.Join(dir, "inkflow.toml")
	contents := "vault_dir = \"" + filepath.Join(dir, "vault") + "\"\n" +
		"state_file = \"state.db\"\n\n" +
		"[ai]\nprovider = \"openai\"\n\n"
	if keyPath != "" {
		contents += "[openai]\napi_key_file = \"" + keyPath + "\"\n\n"
	}
	contents += "[[route]]\nfrom = \"/uploads\"\nai = "
	if wantsAI {
		contents += "true\n"
	} else {
		contents += "false\n"
	}
	if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
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

func TestCheckCommandSuccessAndSamplePreview(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	templates := filepath.Join(dir, "templates")
	if err := os.Mkdir(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(templates, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "inkflow.toml")
	body := "vault_dir = \"" + vault + "\"\ntemplate_dir = \"" + templates + "\"\n\n[[route]]\nfrom = \"/uploads\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd(slog.Default())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--config", configPath, "check", "--sample-filename", "/uploads/2026-05-06 Meeting [finance].pdf"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Resolved routes:", "PDFRel:", "NoteRel:", "tags: [finance]", "title: Meeting", "Preflight successful."} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestCheckCommandReportsPreflightFailures(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "inkflow.toml")
	body := "vault_dir = \"" + filepath.Join(dir, "missing") + "\"\n\n[[route]]\nfrom = \"/uploads\"\nai = true\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd(slog.Default())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--config", configPath, "check"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected preflight failure")
	}
	text := out.String()
	if !strings.Contains(text, "vault_dir") || !strings.Contains(text, "GEMINI_API_KEY") {
		t.Errorf("missing diagnostics:\n%s", text)
	}
}

func TestCheckCommandReturnsLoadErrorForRouteCollision(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "inkflow.toml")
	body := "vault_dir = \"" + dir + "\"\n\n[[route]]\nfrom = \"/a\"\n\n[[route]]\nfrom = \"/a/\"\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd(slog.Default())
	root.SetArgs([]string{"--config", configPath, "check"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected collision load error, got %v", err)
	}
}
