package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Provider.Default != "claude" {
		t.Errorf("Default provider = %q, want claude", c.Provider.Default)
	}
	if c.Provider.Claude.Model == "" {
		t.Errorf("Claude model should have a default")
	}
	if c.Provider.Claude.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("Claude api_key_env = %q", c.Provider.Claude.APIKeyEnv)
	}
}

func TestLoad_Missing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("Load missing should not error: %v", err)
	}
	if c.Provider.Default != "claude" {
		t.Errorf("missing file should return defaults; got %+v", c)
	}
}

func TestLoad_OverridesAndPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[provider]
default = "ollama"

[provider.ollama]
model = "llama3.1:70b"

[ui]
token_budget = 50000
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Provider.Default != "ollama" {
		t.Errorf("Default not overridden: %q", c.Provider.Default)
	}
	if c.Provider.Ollama.Model != "llama3.1:70b" {
		t.Errorf("Ollama model not overridden: %q", c.Provider.Ollama.Model)
	}
	// Untouched ollama field should keep its default.
	if c.Provider.Ollama.Host != "http://localhost:11434" {
		t.Errorf("Ollama host should keep default: %q", c.Provider.Ollama.Host)
	}
	// Untouched provider should keep its defaults.
	if c.Provider.Claude.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("Claude env should keep default: %q", c.Provider.Claude.APIKeyEnv)
	}
	if c.UI.TokenBudget != 50000 {
		t.Errorf("TokenBudget = %d", c.UI.TokenBudget)
	}
}

func TestLoad_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.toml")
	if err := os.WriteFile(path, []byte("this is = = not toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDefaultPath_XDGHonored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xdg/explore/config.toml" {
		t.Errorf("DefaultPath = %q", p)
	}
}
