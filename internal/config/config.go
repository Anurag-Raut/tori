package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	// UsageTTL is the cache lifetime before a usage reading is fetched again.
	UsageTTL = 180 * time.Second

	DisclaimerHead = "Usage meters are off by default. When on, tori reads each saved account's usage by " +
		"calling the provider's own usage endpoint with that account's saved login, from " +
		"outside the official app. For accounts you aren't using, it may also refresh their " +
		"saved access token. Nothing goes anywhere else."
	DisclaimerTerms = "Each provider's terms govern your accounts:"
	DisclaimerTail  = "Turn this on only if you accept that risk. You can turn it off any time."
)

// ToolKeys lists the canonical provider keys in display order.
var ToolKeys = []string{"claude", "codex", "antigravity"}

// DisclaimerTool maps provider keys to their provider-specific terms warning.
var DisclaimerTool = map[string]string{
	"claude":      "Claude: Anthropic limits subscription sign-in to Claude Code and its own apps.",
	"codex":       "Codex: OpenAI's terms prohibit circumventing rate limits.",
	"antigravity": "Antigravity: Google's terms say using third-party tools to access the service with Antigravity sign-in is a breach and may lead to suspension.",
}

// Config represents ~/.tori/config.json.
type Config struct {
	Usage map[string]bool `json:"usage"`
}

// HomeDir resolves the user's home directory, respecting $HOME.
func HomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// RootDir returns TORI_HOME if set, or ~/.tori, along with whether TORI_HOME was overridden.
func RootDir() (root string, overridden bool) {
	if env := os.Getenv("TORI_HOME"); env != "" {
		return env, true
	}
	return filepath.Join(HomeDir(), ".tori"), false
}

// Path returns the config.json path under root.
func Path(root string) string {
	return filepath.Join(root, "config.json")
}

// Load reads config.json from root. Missing or corrupt config defaults to all usage meters off.
func Load(root string) Config {
	var cfg Config
	if !store.ReadJSON(Path(root), &cfg) {
		cfg = Config{}
	}
	if cfg.Usage == nil {
		cfg.Usage = make(map[string]bool)
	}
	return cfg
}

// UsageEnabled returns true only when config.json explicitly sets usage.<key> = true.
func UsageEnabled(root, key string) bool {
	cfg := Load(root)
	return cfg.Usage[key]
}

// SetUsage updates usage opt-in flags for the given keys and saves config.json atomically (0600).
func SetUsage(root string, keys []string, enabled bool) error {
	raw := store.ReadJSONMap(Path(root))
	if raw == nil {
		raw = make(map[string]any)
	}
	u, ok := raw["usage"].(map[string]any)
	if !ok || u == nil {
		u = make(map[string]any)
	}
	for _, k := range ToolKeys {
		if _, exists := u[k]; !exists {
			u[k] = false
		}
	}
	for _, k := range keys {
		u[k] = enabled
	}
	raw["usage"] = u
	return store.WriteJSONAtomic(Path(root), raw, 0o600, false)
}

// DisclaimerLines returns the opt-in text for the given tool keys as paragraphs/bullets.
func DisclaimerLines(keys []string) []string {
	lines := make([]string, 0, len(keys)+3)
	lines = append(lines, DisclaimerHead, DisclaimerTerms)
	for _, k := range keys {
		if text, ok := DisclaimerTool[k]; ok {
			lines = append(lines, "- "+text)
		}
	}
	lines = append(lines, DisclaimerTail)
	return lines
}
