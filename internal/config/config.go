package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Anurag-Raut/tori/internal/store"
)

const (
	// UsageTTL is the cache lifetime before a usage reading is fetched automatically.
	UsageTTL = 180 * time.Second

	// UsageMinRefresh is the minimum interval between manual refreshes (--refresh / r)
	// for a slot that was just fetched, preventing rapid retries from hammering APIs.
	UsageMinRefresh = 30 * time.Second

	// UsageRateLimitBase and UsageRateLimitMax define exponential backoff on HTTP 429.
	UsageRateLimitBase = 60 * time.Second
	UsageRateLimitMax  = 15 * time.Minute

	// UsageRetryAfterCap bounds any server-provided Retry-After header.
	UsageRetryAfterCap = 60 * time.Minute

	// UsageErrorBase and UsageErrorMax define exponential backoff on transient errors
	// (5xx or network/offline failures).
	UsageErrorBase = 30 * time.Second
	UsageErrorMax  = 5 * time.Minute

	// UsageAuthErrorCooldown avoids hammering endpoints when credentials are expired/invalid.
	UsageAuthErrorCooldown = 60 * time.Second

	DisclaimerHead = "Reads quota directly from the provider using your saved logins (may refresh expired tokens). Nothing is sent anywhere else."
	DisclaimerTail = "You can turn this off any time."
)

// ToolKeys lists the canonical provider keys in display order.
var ToolKeys = []string{"claude", "codex", "antigravity"}

// DisclaimerTool maps provider keys with notable terms risks to a short warning.
var DisclaimerTool = map[string]string{
	"antigravity": "Antigravity: Google's terms prohibit third-party tools using Antigravity sign-in and may lead to account suspension.",
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

// DisclaimerLines returns concise opt-in text for the given tool keys.
func DisclaimerLines(keys []string) []string {
	lines := make([]string, 0, len(keys)+2)
	lines = append(lines, DisclaimerHead)
	for _, k := range keys {
		if text, ok := DisclaimerTool[k]; ok {
			lines = append(lines, "Warning: "+text)
		}
	}
	lines = append(lines, DisclaimerTail)
	return lines
}
