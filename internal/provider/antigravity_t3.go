package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// T3 Code runs Google's own antigravity-acp runtime. Tori starts that runtime
// the way T3 Code does and lets Google's code write the login. Tori never reads,
// copies or writes a T3 Code login. The only file access in T3 Code's profile is
// a stat of acp_token.json, creating folders, and writing the small
// settings.json (only when it is missing), which holds no secret.

const (
	t3TargetPrefix  = "t3:"
	t3DefaultID     = "antigravity"
	t3AuthOAuth     = "oauth-personal"
	t3ProfileConfig = `{"auth":{"type":"oauth-personal"}}` + "\n"
)

// t3Instance is one T3 Code Antigravity provider instance that signs in with Google.
type t3Instance struct {
	ID          string
	DisplayName string
	BinaryPath  string
}

// t3BaseDir is T3 Code's data folder: $T3CODE_HOME, else <home>/.t3.
func t3BaseDir(env *Env) string {
	if v := strings.TrimSpace(env.getenv("T3CODE_HOME")); v != "" {
		return v
	}
	if env.Home == "" {
		return ""
	}
	return filepath.Join(env.Home, ".t3")
}

func t3StateDir(env *Env) string {
	base := t3BaseDir(env)
	if base == "" {
		return ""
	}
	return filepath.Join(base, "userdata")
}

// t3ProfileDir is the private folder T3 Code gives one instance (its GEMINI_HOME).
func t3ProfileDir(env *Env, instanceID string) string {
	state := t3StateDir(env)
	if state == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(instanceID))
	return filepath.Join(state, "providers", "antigravity", hex.EncodeToString(sum[:]))
}

type t3SettingsFile struct {
	ProviderInstances map[string]struct {
		Driver      string          `json:"driver"`
		DisplayName string          `json:"displayName"`
		Enabled     *bool           `json:"enabled"`
		Config      json.RawMessage `json:"config"`
	} `json:"providerInstances"`
}

// t3Instances lists the enabled Antigravity instances that use Google sign-in.
func t3Instances(env *Env) []t3Instance {
	state := t3StateDir(env)
	if state == "" {
		return nil
	}
	var out []t3Instance
	seen := false
	if raw, err := os.ReadFile(filepath.Join(state, "settings.json")); err == nil {
		var doc t3SettingsFile
		if json.Unmarshal(raw, &doc) == nil {
			for id, in := range doc.ProviderInstances {
				if in.Driver != "antigravity" {
					continue
				}
				seen = true
				var cfg struct {
					Enabled    *bool  `json:"enabled"`
					AuthMethod string `json:"authMethod"`
					BinaryPath string `json:"binaryPath"`
				}
				if len(in.Config) > 0 {
					_ = json.Unmarshal(in.Config, &cfg)
				}
				// T3 Code keeps Antigravity off unless it is switched on.
				if in.Enabled != nil && !*in.Enabled || cfg.Enabled != nil && !*cfg.Enabled {
					continue
				}
				if in.Enabled == nil && cfg.Enabled == nil {
					continue
				}
				if cfg.AuthMethod != "" && cfg.AuthMethod != t3AuthOAuth {
					continue
				}
				out = append(out, t3Instance{ID: id, DisplayName: strings.TrimSpace(in.DisplayName), BinaryPath: strings.TrimSpace(cfg.BinaryPath)})
			}
		}
	}
	if !seen {
		if st, err := os.Stat(t3ProfileDir(env, t3DefaultID)); err == nil && st.IsDir() {
			out = append(out, t3Instance{ID: t3DefaultID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
