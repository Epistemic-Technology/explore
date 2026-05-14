// Package config loads the user's optional TOML config from
// ~/.config/explore/config.toml (or $XDG_CONFIG_HOME/explore/config.toml).
//
// Resolution order in main.go is: built-in defaults → file values → CLI
// flags. The Default() value is what ships, the file overlays it, and main
// then overlays explicit flags. Empty strings/zero ints in CLI flags are
// treated as "no override" so users who don't pass a flag don't accidentally
// clobber their config.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the root of the TOML schema.
type Config struct {
	Provider ProviderSection `toml:"provider"`
	UI       UISection       `toml:"ui"`
}

// ProviderSection picks a default backend and carries per-backend settings.
// Empty Default means "claude".
type ProviderSection struct {
	Default string         `toml:"default"`
	Claude  ClaudeConfig   `toml:"claude"`
	OpenAI  OpenAIConfig   `toml:"openai"`
	Ollama  OllamaConfig   `toml:"ollama"`
}

// ClaudeConfig configures the Anthropic backend. APIKeyEnv names the env var
// to read the key from; defaults to ANTHROPIC_API_KEY.
type ClaudeConfig struct {
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
}

// OpenAIConfig configures the OpenAI backend. Endpoint allows pointing at
// Azure-compatible proxies; empty means the public API.
type OpenAIConfig struct {
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
	Endpoint  string `toml:"endpoint"`
}

// OllamaConfig configures the local Ollama backend.
type OllamaConfig struct {
	Model string `toml:"model"`
	Host  string `toml:"host"`
}

// UISection is non-provider runtime knobs.
type UISection struct {
	TokenBudget int  `toml:"token_budget"`
	NoLSP       bool `toml:"no_lsp"`

	// LongFunctionThreshold is the line count above which a function's
	// explanation prompt asks the LLM for a structural outline instead of a
	// 3-6 sentence summary. 0 disables the feature.
	LongFunctionThreshold int `toml:"long_function_threshold"`
}

// Default returns the built-in configuration — what you'd get with no
// config file and no CLI overrides. Mirrors the per-provider defaults
// hardcoded in each adapter's New().
func Default() Config {
	return Config{
		Provider: ProviderSection{
			Default: "claude",
			Claude: ClaudeConfig{
				Model:     "claude-sonnet-4-6",
				APIKeyEnv: "ANTHROPIC_API_KEY",
			},
			OpenAI: OpenAIConfig{
				Model:     "gpt-4o-mini",
				APIKeyEnv: "OPENAI_API_KEY",
			},
			Ollama: OllamaConfig{
				Model: "qwen2.5-coder:14b",
				Host:  "http://localhost:11434",
			},
		},
		UI: UISection{
			LongFunctionThreshold: 200,
		},
	}
}

// Load reads a TOML file, overlaying its non-zero fields on top of Default().
// Returns (Default(), nil) when path is empty or the file doesn't exist;
// returns an error for explicit parse failures so typos are loud.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Default(), fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// DefaultPath returns the conventional location of the config file:
// $XDG_CONFIG_HOME/explore/config.toml if set, else ~/.config/explore/config.toml.
// Empty string + nil error means "no home dir found" — caller treats as "no
// config" rather than an error.
func DefaultPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "explore", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil
	}
	return filepath.Join(home, ".config", "explore", "config.toml"), nil
}
