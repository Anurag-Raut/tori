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
	"sync"
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
	if err := config.SetUsage(env.Root, config.ToolKeys, false); err != nil {
		t.Fatal(err)
	}

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

	// Clean machine without claude installed reports not installed cleanly.
	env.GOOS = "darwin"
	if _, err := c.Add(env, nil); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected not-installed error on clean machine, got %v", err)
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

func TestDefaultLabelDeduplicatesAliasMatchingName(t *testing.T) {
	got := DefaultLabel("user@example.com", store.Meta{Email: "user@example.com", Alias: "User@Example.com", Plan: "Pro"})
	if got != "user@example.com · Pro" {
		t.Fatalf("DefaultLabel duplicated alias: got %q", got)
	}
	got2 := DefaultLabel("user@example.com", store.Meta{Email: "user@example.com", Alias: "Work", Plan: "Pro"})
	if got2 != "user@example.com [Work] · Pro" {
		t.Fatalf("DefaultLabel dropped distinct alias: got %q", got2)
	}
}

func TestClearLiveAndRestoreAllProviders(t *testing.T) {
	env, envMap := newTestEnv(t, nil)

	// --- Claude Code: preserves non-auth keys in both files ---
	c := &Claude{}
	credsPath := filepath.Join(env.Home, ".claude", ".credentials.json")
	cfgPath := filepath.Join(env.Home, ".claude.json")
	_ = store.WriteJSONAtomic(credsPath, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "at-1", "refreshToken": "rt-1", "subscriptionType": "pro"},
		"mcpOAuth":      map[string]any{"keep": true},
	}, 0o600, false)
	_ = store.WriteJSONAtomic(cfgPath, map[string]any{
		"oauthAccount": map[string]any{"accountUuid": "uuid-1", "emailAddress": "dev@example.com"},
		"theme":        "dark",
	}, 0o600, false)
	if _, err := c.Add(env, nil); err != nil {
		t.Fatal(err)
	}
	restoreC, err := c.ClearLive(env)
	if err != nil {
		t.Fatalf("claude ClearLive failed: %v", err)
	}
	st, _ := c.Status(env)
	if st.LoggedIn {
		t.Fatal("expected claude logged out after ClearLive")
	}
	if m := store.ReadJSONMap(credsPath); m["mcpOAuth"] == nil {
		t.Fatal("expected mcpOAuth preserved in .credentials.json")
	}
	if m := store.ReadJSONMap(cfgPath); m["theme"] != "dark" {
		t.Fatal("expected theme preserved in .claude.json")
	}
	if err := restoreC(); err != nil {
		t.Fatalf("claude restore failed: %v", err)
	}
	st, _ = c.Status(env)
	if !st.LoggedIn || st.DisplayName != "dev@example.com" {
		t.Fatalf("expected claude login restored, got %+v", st)
	}

	// --- Codex ---
	x := &Codex{}
	codexAuth := filepath.Join(env.Home, ".codex", "auth.json")
	_ = store.WriteJSONAtomic(codexAuth, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"account_id":    "acct-x1",
			"access_token":  "at-x1",
			"refresh_token": "rt-x1",
			"id_token":      makeJWT(map[string]any{"email": "codex@example.com"}),
		},
	}, 0o600, false)
	if _, err := x.Add(env, nil); err != nil {
		t.Fatal(err)
	}
	restoreX, err := x.ClearLive(env)
	if err != nil {
		t.Fatalf("codex ClearLive failed: %v", err)
	}
	if st, _ := x.Status(env); st.LoggedIn {
		t.Fatal("expected codex logged out after ClearLive")
	}
	if err := restoreX(); err != nil {
		t.Fatalf("codex restore failed: %v", err)
	}
	if st, _ := x.Status(env); !st.LoggedIn || st.DisplayName != "codex@example.com" {
		t.Fatalf("expected codex restored, got %+v", st)
	}

	// --- Antigravity ---
	g := &Antigravity{}
	agyPath := filepath.Join(env.Home, "agy", "acp_token.json")
	envMap["TORI_AGY_TOKEN"] = agyPath
	_ = store.WriteJSONAtomic(agyPath, map[string]any{"refresh_token": "rt-g1"}, 0o600, false)
	if _, err := g.Add(env, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	restoreG, err := g.ClearLive(env)
	if err != nil {
		t.Fatalf("antigravity ClearLive failed: %v", err)
	}
	if st, _ := g.Status(env); st.LoggedIn {
		t.Fatal("expected antigravity logged out after ClearLive")
	}
	if err := restoreG(); err != nil {
		t.Fatalf("antigravity restore failed: %v", err)
	}
	if st, _ := g.Status(env); !st.LoggedIn || st.ManagedSlot == nil || *st.ManagedSlot != 1 {
		t.Fatalf("expected antigravity restored to slot 1, got %+v", st)
	}
}

func seedCodexForUsageTest(t *testing.T, env *Env) (*Codex, store.Slot) {
	t.Helper()
	x := &Codex{}
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".codex", "auth.json"), map[string]any{
		"tokens": map[string]any{
			"access_token":  "at-codex-rl",
			"refresh_token": "rt-codex-rl",
			"account_id":    "acct-rl-1",
			"id_token": makeJWT(map[string]any{
				"email": "rl@example.com",
				"https://api.openai.com/auth": map[string]any{
					"chatgpt_account_id": "acct-rl-1",
					"chatgpt_plan_type":  "plus",
				},
			}),
		},
	}, 0o600, false)
	if _, err := x.Add(env, nil); err != nil {
		t.Fatal(err)
	}
	if err := config.SetUsage(env.Root, []string{"codex"}, true); err != nil {
		t.Fatal(err)
	}
	slots, err := x.Slots(env)
	if err != nil || len(slots) != 1 {
		t.Fatalf("expected 1 codex slot, got %v %v", slots, err)
	}
	return x, slots[0]
}

func TestUsageManualRefreshCooldown(t *testing.T) {
	var calls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(200, map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 20.0},
			},
		}), nil
	})
	env, _ := newTestEnv(t, rt)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	env.Now = func() time.Time { return now }
	x, slot := seedCodexForUsageTest(t, env)

	// First fetch hits the network.
	info := UsageForSlot(env, x, slot, true, nil, true, true)
	if info == nil || info.Throttled || calls.Load() != 1 {
		t.Fatalf("expected 1 fresh fetch, got info=%+v calls=%d", info, calls.Load())
	}

	// Immediate manual retry (force=true) is throttled by UsageMinRefresh.
	now = now.Add(5 * time.Second)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if info == nil || !info.Throttled || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds < 20 {
		t.Fatalf("expected manual refresh cooldown, got %+v", info)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 0 extra HTTP calls during cooldown, got %d", calls.Load())
	}

	// Advancing past UsageMinRefresh allows another manual refresh.
	now = now.Add(config.UsageMinRefresh)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if info == nil || info.Throttled || calls.Load() != 2 {
		t.Fatalf("expected 2nd fetch after cooldown, got info=%+v calls=%d", info, calls.Load())
	}
}

func TestUsageRateLimitBackoffAndRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var mode atomic.Int32 // 0=200(40%), 1=429(Retry-After:120), 2=429(no header), 3=200(55%)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		switch mode.Load() {
		case 0:
			return jsonResponse(200, map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{"used_percent": 40.0},
				},
			}), nil
		case 1:
			resp := jsonResponse(429, map[string]any{"error": "rate_limited"})
			resp.Header.Set("Retry-After", "120")
			return resp, nil
		case 2:
			return jsonResponse(429, map[string]any{"error": "rate_limited"}), nil
		default:
			return jsonResponse(200, map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{"used_percent": 55.0},
				},
			}), nil
		}
	})
	env, _ := newTestEnv(t, rt)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	env.Now = func() time.Time { return now }
	x, slot := seedCodexForUsageTest(t, env)

	// Initial success caches 40%.
	info := UsageForSlot(env, x, slot, true, nil, true, false)
	if info == nil || len(info.Windows) != 1 || info.Windows[0].Pct != 40.0 {
		t.Fatalf("expected initial 40%% window, got %+v", info)
	}

	// Expire cache and return HTTP 429 with Retry-After: 120.
	now = now.Add(config.UsageTTL + 10*time.Second)
	mode.Store(1)
	info = UsageForSlot(env, x, slot, true, nil, true, false)
	if info == nil || !info.Throttled || len(info.Windows) != 1 || info.Windows[0].Pct != 40.0 {
		t.Fatalf("expected cached 40%% window preserved on 429, got %+v", info)
	}
	if info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 120 {
		t.Fatalf("expected RetryAfterSeconds=120 from Retry-After header, got %+v", info.RetryAfterSeconds)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 total HTTP calls, got %d", calls.Load())
	}

	// Repeated force retries during the 120s cooldown must make ZERO HTTP calls.
	now = now.Add(30 * time.Second)
	for i := 0; i < 3; i++ {
		info = UsageForSlot(env, x, slot, true, nil, true, true)
		if info == nil || !info.Throttled || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 90 {
			t.Fatalf("retry %d: expected throttled with 90s remaining, got %+v", i, info)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 0 HTTP calls during 429 cooldown, got %d total", calls.Load())
	}

	// Advance past 120s; second 429 (no Retry-After header) triggers 2x exponential backoff (120s).
	now = now.Add(95 * time.Second)
	mode.Store(2)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 3 {
		t.Fatalf("expected 3rd HTTP call after cooldown elapsed, got %d", calls.Load())
	}
	if info == nil || !info.Throttled || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 120 {
		t.Fatalf("expected 2x exponential backoff (120s) on 2nd 429, got %+v", info)
	}

	// Advance past backoff; 200 clears FailCount and CooldownUntil.
	now = now.Add(125 * time.Second)
	mode.Store(3)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 4 || info == nil || info.Throttled || info.RetryAfterSeconds != nil || info.Windows[0].Pct != 55.0 {
		t.Fatalf("expected clean recovery to 55%%, got info=%+v calls=%d", info, calls.Load())
	}
	var disk UsageCache
	if !store.ReadJSON(filepath.Join(slot.Dir, "usage.json"), &disk) || disk.FailCount != 0 || disk.CooldownUntil != 0 {
		t.Fatalf("expected persisted backoff cleared after success, got %+v", disk)
	}
}

func TestUsageTransientErrorExponentialBackoff(t *testing.T) {
	var calls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(503, map[string]any{"error": "unavailable"}), nil
	})
	env, _ := newTestEnv(t, rt)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	env.Now = func() time.Time { return now }
	x, slot := seedCodexForUsageTest(t, env)

	// 1st 503 -> 30s backoff.
	info := UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 1 || info == nil || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 30 {
		t.Fatalf("expected 30s backoff on 1st 503, got info=%+v calls=%d", info, calls.Load())
	}
	// Retry at +10s -> blocked.
	now = now.Add(10 * time.Second)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 1 || !info.Throttled {
		t.Fatalf("expected blocked during 1st backoff, got info=%+v calls=%d", info, calls.Load())
	}

	// 2nd 503 at +31s -> 60s backoff.
	now = now.Add(21 * time.Second)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 2 || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 60 {
		t.Fatalf("expected 60s backoff on 2nd 503, got info=%+v calls=%d", info, calls.Load())
	}

	// 3rd 503 after +61s -> 120s backoff.
	now = now.Add(61 * time.Second)
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 3 || info.RetryAfterSeconds == nil || *info.RetryAfterSeconds != 120 {
		t.Fatalf("expected 120s backoff on 3rd 503, got info=%+v calls=%d", info, calls.Load())
	}
}

func TestUsageSingleflightCoalescesConcurrentFetches(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return jsonResponse(200, map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 25.0},
			},
		}), nil
	})
	env, _ := newTestEnv(t, rt)
	x, slot := seedCodexForUsageTest(t, env)

	const workers = 5
	results := make([]*UsageInfo, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = UsageForSlot(env, x, slot, true, nil, true, true)
		}(i)
	}
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected 5 concurrent callers to coalesce into 1 HTTP call, got %d", calls.Load())
	}
	for i, res := range results {
		if res == nil || len(res.Windows) != 1 || res.Windows[0].Pct != 25.0 {
			t.Fatalf("worker %d: unexpected result %+v", i, res)
		}
	}
}

func TestClearUsageCooldownOnReAdd(t *testing.T) {
	var calls atomic.Int32
	var okStatus atomic.Bool
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !okStatus.Load() {
			return jsonResponse(401, map[string]any{"error": "unauthorized"}), nil
		}
		return jsonResponse(200, map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 15.0},
			},
		}), nil
	})
	env, _ := newTestEnv(t, rt)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	env.Now = func() time.Time { return now }
	x, slot := seedCodexForUsageTest(t, env)

	// 401 sets UsageAuthErrorCooldown (60s).
	info := UsageForSlot(env, x, slot, true, nil, true, true)
	if info == nil || info.Error == nil || *info.Error != "login expired" || calls.Load() != 1 {
		t.Fatalf("expected login expired on 401, got info=%+v calls=%d", info, calls.Load())
	}

	// Re-adding (updating credentials) clears the auth error cooldown immediately.
	okStatus.Store(true)
	if _, err := x.Add(env, nil); err != nil {
		t.Fatal(err)
	}
	info = UsageForSlot(env, x, slot, true, nil, true, true)
	if calls.Load() != 2 || info == nil || info.Error != nil || len(info.Windows) != 1 || info.Windows[0].Pct != 15.0 {
		t.Fatalf("expected immediate fetch after credential update, got info=%+v calls=%d", info, calls.Load())
	}
}
