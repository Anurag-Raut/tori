package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(b)),
	}
}

func makeJWT(payload map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(payload)
	pl := base64.RawURLEncoding.EncodeToString(b)
	return hdr + "." + pl + ".sig"
}

func newTestEnv(t *testing.T, rt http.RoundTripper) (*Env, map[string]string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".tori")
	envMap := map[string]string{}
	now := time.Unix(1770000000, 0).UTC()
	env := &Env{
		Root:           root,
		RootOverridden: false,
		Home:           home,
		HTTPClient:     &http.Client{Transport: rt, Timeout: 5 * time.Second},
		Now:            func() time.Time { return now },
		GOOS:           "linux",
		GOARCH:         "amd64",
		Warn:           func(string) {},
		Environ: func(k string) string {
			return envMap[k]
		},
	}
	return env, envMap
}

func TestNoNetworkWhenUsageOff(t *testing.T) {
	var calls atomic.Int32
	forbidNet := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		t.Errorf("unexpected network request when usage is off: %s %s", req.Method, req.URL.String())
		return nil, errors.New("network forbidden")
	})

	env, envMap := newTestEnv(t, forbidNet)

	// Setup live credentials for Claude, Codex, and Antigravity.
	claudeCreds := filepath.Join(env.Home, ".claude", ".credentials.json")
	claudeCfg := filepath.Join(env.Home, ".claude.json")
	_ = store.WriteJSONAtomic(claudeCreds, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "tok-c1",
			"refreshToken":     "rt-c1",
			"expiresAt":        float64(env.now().Unix()+3600) * 1000,
			"subscriptionType": "pro",
		},
	}, 0o600, false)
	_ = store.WriteJSONAtomic(claudeCfg, map[string]any{
		"oauthAccount": map[string]any{
			"accountUuid":  "uuid-c1",
			"emailAddress": "alice@example.com",
		},
	}, 0o600, false)

	codexAuth := filepath.Join(env.Home, ".codex", "auth.json")
	_ = store.WriteJSONAtomic(codexAuth, map[string]any{
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.now().Unix() + 3600}),
			"refresh_token": "rt-x1",
			"account_id":    "acct-x1",
			"id_token": makeJWT(map[string]any{
				"email": "bob@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_plan_type":  "plus",
					"chatgpt_account_id": "acct-x1",
				},
			}),
		},
	}, 0o600, false)

	agyToken := filepath.Join(env.Home, "agy", "acp_token.json")
	envMap["TORI_AGY_TOKEN"] = agyToken
	_ = store.WriteJSONAtomic(agyToken, map[string]any{
		"client_id":     "cid",
		"client_secret": "csec",
		"refresh_token": "rt-g1",
		"project_id":    "proj-1",
	}, 0o600, false)

	for _, p := range All() {
		if _, err := p.Add(env, []string{"work"}); err != nil {
			t.Fatalf("%s Add failed: %v", p.Key(), err)
		}
		entries, cur, err := Entries(env, p, true, true)
		if err != nil {
			t.Fatalf("%s Entries failed: %v", p.Key(), err)
		}
		if cur == nil || *cur != 1 || len(entries) != 1 || entries[0].Usage != nil {
			t.Fatalf("%s unexpected entries with usage off: cur=%v entries=%+v", p.Key(), cur, entries)
		}
		if _, err := p.Status(env); err != nil {
			t.Fatalf("%s Status failed: %v", p.Key(), err)
		}
	}

	// Even if usage is turned ON in config.json, ForceUsageOff or DisableNetwork must make zero HTTP requests.
	if err := config.SetUsage(env.Root, config.ToolKeys, true); err != nil {
		t.Fatal(err)
	}
	env.ForceUsageOff = true
	for _, p := range All() {
		entries, _, err := Entries(env, p, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if entries[0].Usage != nil {
			t.Fatalf("expected nil usage when ForceUsageOff is true for %s", p.Key())
		}
	}
	env.ForceUsageOff = false
	env.DisableNetwork = true
	for _, p := range All() {
		if _, _, err := Entries(env, p, true, true); err != nil {
			t.Fatal(err)
		}
	}

	if calls.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", calls.Load())
	}
}

func TestClaudeLifecycleAndUsage(t *testing.T) {
	var netCalls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		netCalls.Add(1)
		switch req.URL.String() {
		case claudeTokenURL:
			return jsonResponse(200, map[string]any{
				"access_token":  "new-at",
				"refresh_token": "new-rt",
				"expires_in":    7200,
			}), nil
		case claudeUsageURL:
			auth := req.Header.Get("Authorization")
			if auth == "Bearer expired-at" {
				return jsonResponse(401, map[string]any{"error": "unauthorized"}), nil
			}
			return jsonResponse(200, map[string]any{
				"five_hour": map[string]any{"utilization": 42.5, "resets_at": "2026-11-25T15:00:00Z"},
				"seven_day": map[string]any{"utilization": 88.0, "resets_at": "2026-11-30T00:00:00Z"},
			}), nil
		default:
			t.Fatalf("unexpected URL: %s", req.URL.String())
			return nil, nil
		}
	})

	env, _ := newTestEnv(t, rt)
	c := &Claude{}

	// macOS Keychain hint when not logged in.
	env.GOOS = "darwin"
	if _, err := c.Add(env, nil); err == nil || !strings.Contains(err.Error(), "Keychain") {
		t.Fatalf("expected darwin Keychain error, got %v", err)
	}
	env.GOOS = "linux"

	credsPath, cfgPath := c.livePaths(env)
	// Write .claude.json with 0644 and extra key to verify keep=true and key preservation.
	_ = os.MkdirAll(filepath.Dir(credsPath), 0o700)
	_ = store.WriteJSONAtomic(cfgPath, map[string]any{
		"numStartups": 9,
		"oauthAccount": map[string]any{
			"accountUuid":  "uuid-1",
			"emailAddress": "alice@example.com",
		},
	}, 0o644, false)
	_ = store.WriteJSONAtomic(credsPath, map[string]any{
		"otherCred": "keep-me",
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-1",
			"refreshToken":     "rt-1",
			"expiresAt":        float64(env.now().Unix()+3600) * 1000,
			"subscriptionType": "max",
		},
	}, 0o600, false)

	out, err := c.Add(env, []string{"Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "Added slot 1: alice@example.com [Work] · Max") {
		t.Fatalf("unexpected Add output: %v", out)
	}

	// Second account with expired token (inactive when we switch back to slot 1).
	_ = store.WriteJSONAtomic(cfgPath, map[string]any{
		"numStartups": 9,
		"oauthAccount": map[string]any{
			"accountUuid":  "uuid-2",
			"emailAddress": "bob@example.com",
		},
	}, 0o644, true)
	_ = store.WriteJSONAtomic(credsPath, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "expired-at",
			"refreshToken":     "rt-2",
			"expiresAt":        float64(env.now().Unix()-100) * 1000,
			"subscriptionType": "pro",
		},
	}, 0o600, false)

	if _, err := c.Add(env, []string{"Personal"}); err != nil {
		t.Fatal(err)
	}

	// Switch to slot 1.
	if _, err := Switch(env, c, "1"); err != nil {
		t.Fatal(err)
	}
	// Verify unrelated keys & 0644 perm preserved in .claude.json and .credentials.json.
	liveCfg := store.ReadJSONMap(cfgPath)
	if liveCfg["numStartups"] != float64(9) {
		t.Fatalf("expected numStartups preserved, got %v", liveCfg["numStartups"])
	}
	st, err := os.Stat(cfgPath)
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("expected .claude.json perm 0644 preserved, got %v err=%v", st.Mode().Perm(), err)
	}

	// Enable usage for Claude and fetch both slots (slot 2 should refresh token).
	if err := config.SetUsage(env.Root, []string{"claude"}, true); err != nil {
		t.Fatal(err)
	}
	entries, cur, err := Entries(env, c, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || *cur != 1 || len(entries) != 2 {
		t.Fatalf("unexpected entries: cur=%v len=%d", cur, len(entries))
	}
	if entries[0].Usage == nil || len(entries[0].Usage.Windows) != 2 || entries[0].Usage.Windows[0].Pct != 42.5 {
		t.Fatalf("unexpected slot 1 usage: %+v", entries[0].Usage)
	}
	if entries[1].Usage == nil || entries[1].Usage.Error != nil || entries[1].Usage.Windows[1].Pct != 88.0 {
		t.Fatalf("unexpected slot 2 usage after refresh: %+v", entries[1].Usage)
	}

	// Second call within 180s TTL should hit cache and make zero additional network calls.
	before := netCalls.Load()
	if _, _, err := Entries(env, c, true, false); err != nil {
		t.Fatal(err)
	}
	if netCalls.Load() != before {
		t.Fatalf("expected cached usage to make 0 HTTP calls, made %d", netCalls.Load()-before)
	}
}

func TestCodexAPIKeyAndOAuthUsage(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexUsageURL {
			t.Fatalf("unexpected URL: %s", req.URL.String())
		}
		if req.Header.Get("ChatGPT-Account-Id") != "acct-1" {
			t.Fatalf("missing ChatGPT-Account-Id header: %v", req.Header)
		}
		return jsonResponse(200, map[string]any{
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 15.0, "limit_window_seconds": 18000, "reset_after_seconds": 600},
				"secondary_window": map[string]any{"used_percent": 55.0, "limit_window_seconds": 2592000, "reset_at": 1771000000},
			},
		}), nil
	})
	env, envMap := newTestEnv(t, rt)
	x := &Codex{}

	codexHome := filepath.Join(env.Home, "custom-codex")
	envMap["CODEX_HOME"] = codexHome
	authPath := filepath.Join(codexHome, "auth.json")

	// Slot 1: OAuth
	_ = store.WriteJSONAtomic(authPath, map[string]any{
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.now().Unix() + 3600}),
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
			"id_token": makeJWT(map[string]any{
				"email": "dev@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_plan_type": "team",
				},
			}),
		},
	}, 0o600, false)
	if _, err := x.Add(env, []string{"Work"}); err != nil {
		t.Fatal(err)
	}

	// Slot 2: API key
	_ = store.WriteJSONAtomic(authPath, map[string]any{
		"auth_mode":      "apikey",
		"OPENAI_API_KEY": "sk-test-1234",
	}, 0o600, false)
	if _, err := x.Add(env, []string{"Key"}); err != nil {
		t.Fatal(err)
	}

	if err := config.SetUsage(env.Root, []string{"codex"}, true); err != nil {
		t.Fatal(err)
	}
	entries, cur, err := Entries(env, x, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || *cur != 2 {
		t.Fatalf("expected active slot 2, got %v", cur)
	}
	if len(entries[0].Usage.Windows) != 2 || entries[0].Usage.Windows[0].Label != "5h" || entries[0].Usage.Windows[1].Label != "30d" {
		t.Fatalf("unexpected codex windows: %+v", entries[0].Usage.Windows)
	}
	if entries[1].Usage != nil || entries[1].Note != "API key (no usage)" {
		t.Fatalf("expected no usage for API key slot, got %+v", entries[1])
	}
}

func TestAntigravityEmailLearnAndQuotaFallback(t *testing.T) {
	var summaryTried atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case agyGoogleTokenURL:
			return jsonResponse(200, map[string]any{"access_token": "agy-at", "expires_in": 3600}), nil
		case agyUserinfoURL:
			return jsonResponse(200, map[string]any{"email": "gemini@example.com"}), nil
		case agyDailyEndpoint + "/v1internal:retrieveUserQuotaSummary":
			summaryTried.Add(1)
			return jsonResponse(200, map[string]any{}), nil // empty -> fall back
		case agyProdEndpoint + "/v1internal:retrieveUserQuotaSummary":
			return jsonResponse(500, map[string]any{"error": "boom"}), nil
		case agyDailyEndpoint + "/v1internal:fetchAvailableModels":
			return jsonResponse(200, map[string]any{
				"models": map[string]any{
					"gemini-2.5-pro": map[string]any{
						"modelProvider":   "MODEL_PROVIDER_GOOGLE",
						"quotaInfo":       map[string]any{"remainingFraction": 0.75, "resetTime": "2026-11-25T16:00:00Z"},
						"weeklyQuotaInfo": map[string]any{"remainingFraction": 0.4, "resetTime": "2026-11-30T00:00:00Z"},
					},
				},
			}), nil
		default:
			t.Fatalf("unexpected URL: %s", req.URL.String())
			return nil, nil
		}
	})

	env, envMap := newTestEnv(t, rt)
	g := &Antigravity{}
	tokPath := filepath.Join(env.Home, "agy", "acp_token.json")
	envMap["TORI_AGY_TOKEN"] = tokPath
	_ = store.WriteJSONAtomic(tokPath, map[string]any{
		"client_id":     "cid",
		"client_secret": "csec",
		"refresh_token": "rt-g1",
		"project_id":    "proj-1",
	}, 0o600, false)

	if _, err := g.Add(env, []string{"primary"}); err != nil {
		t.Fatal(err)
	}
	if err := config.SetUsage(env.Root, []string{"antigravity"}, true); err != nil {
		t.Fatal(err)
	}
	entries, _, err := Entries(env, g, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if summaryTried.Load() == 0 {
		t.Fatal("expected retrieveUserQuotaSummary on daily endpoint first")
	}
	if entries[0].Email != "gemini@example.com" {
		t.Fatalf("expected learned email gemini@example.com, got %q", entries[0].Email)
	}
	if entries[0].Usage == nil || len(entries[0].Usage.Windows) != 2 {
		t.Fatalf("unexpected usage: %+v", entries[0].Usage)
	}
	if entries[0].Usage.Windows[0].Pct != 25.0 || entries[0].Usage.Windows[1].Pct != 60.0 {
		t.Fatalf("unexpected pct values: %+v", entries[0].Usage.Windows)
	}
}

func TestOrderedProvidersEmptyLast(t *testing.T) {
	all := All()
	// Only Antigravity (index 2) and Codex (index 1) have accounts -> Codex, Antigravity, Claude.
	got := Ordered(all, map[string]int{"claude": 0, "codex": 1, "antigravity": 2})
	want := []string{"codex", "antigravity", "claude"}
	for i, p := range got {
		if p.Key() != want[i] {
			t.Fatalf("Ordered[%d] = %s, want %s", i, p.Key(), want[i])
		}
	}
}
