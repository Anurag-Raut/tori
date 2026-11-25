package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
	"github.com/Anurag-Raut/tori/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func makeJWT(payload map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(payload)
	pl := base64.RawURLEncoding.EncodeToString(b)
	return hdr + "." + pl + ".sig"
}

func setupCLIEnv(t *testing.T, rt http.RoundTripper) (*provider.Env, map[string]string) {
	t.Helper()
	home := t.TempDir()
	envMap := map[string]string{}
	now := time.Unix(1770000000, 0).UTC()
	env := &provider.Env{
		Root:       filepath.Join(home, ".tori"),
		Home:       home,
		HTTPClient: &http.Client{Transport: rt, Timeout: 5 * time.Second},
		Now:        func() time.Time { return now },
		GOOS:       "linux",
		GOARCH:     "amd64",
		Environ:    func(k string) string { return envMap[k] },
	}
	return env, envMap
}

func runCmd(env *provider.Env, stdin string, isTTY bool, args ...string) (int, string, string) {
	var outBuf, errBuf bytes.Buffer
	rc := Run(env, args, "1.2.3", IO{
		Stdin:  strings.NewReader(stdin),
		Stdout: &outBuf,
		Stderr: &errBuf,
		IsTTY:  isTTY,
	}, func(*provider.Env) int { return 0 })
	return rc, outBuf.String(), errBuf.String()
}

func TestVersionHelpAndNonTTYRoot(t *testing.T) {
	env, _ := setupCLIEnv(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("no net")
	}))

	rc, out, _ := runCmd(env, "", false, "--version")
	if rc != 0 || strings.TrimSpace(out) != "tori 1.2.3" {
		t.Fatalf("unexpected --version: rc=%d out=%q", rc, out)
	}

	rc, out, _ = runCmd(env, "", false, "help")
	if rc != 0 || !strings.Contains(out, "tori usage on|off") {
		t.Fatalf("unexpected help: rc=%d out=%q", rc, out)
	}

	rc, _, errOut := runCmd(env, "", false)
	if rc != 2 || !strings.Contains(errOut, "interactive mode needs a terminal") {
		t.Fatalf("expected rc=2 for non-TTY root, got rc=%d err=%q", rc, errOut)
	}
}

func TestUsageOptInFlowAndStrictNoNetwork(t *testing.T) {
	var calls atomic.Int32
	forbidNet := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		t.Errorf("unexpected network call: %s", r.URL.String())
		return nil, errors.New("forbidden")
	})
	env, _ := setupCLIEnv(t, forbidNet)

	// Default status: all off
	rc, out, _ := runCmd(env, "", false, "usage")
	if rc != 0 || !strings.Contains(out, "Claude Code  usage off") {
		t.Fatalf("unexpected usage status: rc=%d out=%q", rc, out)
	}

	// Non-TTY without --yes fails cleanly
	rc, _, errOut := runCmd(env, "", false, "usage", "on", "c")
	if rc != 1 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("expected confirmation failure without --yes: rc=%d err=%q", rc, errOut)
	}
	if config.UsageEnabled(env.Root, "claude") {
		t.Fatal("expected claude usage still off")
	}

	// Interactive TTY cancel with "n"
	rc, out, _ = runCmd(env, "n\n", true, "usage", "on", "c")
	if rc != 0 || !strings.Contains(out, config.DisclaimerHead) || !strings.Contains(out, "Cancelled.") {
		t.Fatalf("unexpected cancel output: rc=%d out=%q", rc, out)
	}
	if config.UsageEnabled(env.Root, "claude") {
		t.Fatal("expected claude usage still off after cancel")
	}

	// --yes turns it on
	rc, out, _ = runCmd(env, "", false, "usage", "on", "c", "--yes")
	if rc != 0 || !strings.Contains(out, "Usage meters on for Claude Code.") {
		t.Fatalf("unexpected usage on output: rc=%d out=%q", rc, out)
	}
	if !config.UsageEnabled(env.Root, "claude") {
		t.Fatal("expected claude usage on")
	}

	// Turn off
	rc, out, _ = runCmd(env, "", false, "usage", "off", "all")
	if rc != 0 || !strings.Contains(out, "Usage meters off") {
		t.Fatalf("unexpected usage off output: rc=%d out=%q", rc, out)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", calls.Load())
	}
}

func TestFullCLICRUDAndJSONAndEmptyLast(t *testing.T) {
	var netCalls atomic.Int32
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		netCalls.Add(1)
		b, _ := json.Marshal(map[string]any{
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 33.0, "limit_window_seconds": 18000, "reset_after_seconds": 300},
			},
		})
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(b)),
		}, nil
	})
	env, _ := setupCLIEnv(t, rt)

	// Add 2 Codex accounts.
	codexAuth := filepath.Join(env.Home, ".codex", "auth.json")
	writeCodex := func(id, email string) {
		_ = store.WriteJSONAtomic(codexAuth, map[string]any{
			"tokens": map[string]any{
				"access_token":  makeJWT(map[string]any{"exp": env.Now().Unix() + 3600}),
				"refresh_token": "rt-" + id,
				"account_id":    id,
				"id_token": makeJWT(map[string]any{
					"email":                       email,
					"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
				}),
			},
		}, 0o600, false)
	}

	writeCodex("acct-1", "one@example.com")
	if rc, out, errOut := runCmd(env, "", false, "x", "add", "Work"); rc != 0 || !strings.Contains(out, "Added slot 1") {
		t.Fatalf("add 1 failed: rc=%d out=%q err=%q", rc, out, errOut)
	}

	writeCodex("acct-2", "two@example.com")
	if rc, out, errOut := runCmd(env, "", false, "codex", "add", "Personal"); rc != 0 || !strings.Contains(out, "Added slot 2") {
		t.Fatalf("add 2 failed: rc=%d out=%q err=%q", rc, out, errOut)
	}

	// Alias update & --unset.
	if rc, out, _ := runCmd(env, "", false, "x", "alias", "1", "Primary"); rc != 0 || !strings.Contains(out, "[Primary]") {
		t.Fatalf("alias failed: rc=%d out=%q", rc, out)
	}

	// Switch by alias.
	if rc, out, _ := runCmd(env, "", false, "x", "switch", "primary"); rc != 0 || !strings.Contains(out, "Switched to slot 1") {
		t.Fatalf("switch by alias failed: rc=%d out=%q", rc, out)
	}

	// `tori list` should show Codex FIRST, and Claude Code / Antigravity LAST!
	rc, listOut, _ := runCmd(env, "", false, "list")
	if rc != 0 {
		t.Fatalf("list failed: %d", rc)
	}
	idxCodex := strings.Index(listOut, "== Codex ==")
	idxClaude := strings.Index(listOut, "== Claude Code ==")
	idxAgy := strings.Index(listOut, "== Antigravity ==")
	if idxCodex == -1 || idxClaude == -1 || idxAgy == -1 || !(idxCodex < idxClaude && idxClaude < idxAgy) {
		t.Fatalf("expected Codex first and Claude < Antigravity last, got:\n%s", listOut)
	}
	if netCalls.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls with usage off, got %d", netCalls.Load())
	}

	// Enable Codex usage and verify --no-usage prevents network while default fetches.
	_ = config.SetUsage(env.Root, []string{"codex"}, true)
	rc, jsonNoUsage, _ := runCmd(env, "", false, "x", "list", "--json", "--no-usage")
	if rc != 0 {
		t.Fatalf("list --json --no-usage failed")
	}
	if netCalls.Load() != 0 {
		t.Fatalf("expected 0 HTTP calls with --no-usage, got %d", netCalls.Load())
	}
	var parsed provider.JSONList
	if err := json.Unmarshal([]byte(jsonNoUsage), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonNoUsage)
	}
	if parsed.SchemaVersion != 1 || parsed.ActiveAccountNumber == nil || *parsed.ActiveAccountNumber != 1 || len(parsed.Accounts) != 2 {
		t.Fatalf("unexpected JSON payload: %+v", parsed)
	}

	// Now fetch with --refresh.
	rc, jsonWithUsage, _ := runCmd(env, "", false, "x", "list", "--json", "--refresh")
	if rc != 0 {
		t.Fatal("list --json --refresh failed")
	}
	if netCalls.Load() != 2 {
		t.Fatalf("expected 2 HTTP calls for 2 codex slots, got %d", netCalls.Load())
	}
	if err := json.Unmarshal([]byte(jsonWithUsage), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Accounts[0].Usage == nil || parsed.Accounts[0].Usage.Windows[0].Pct != 33.0 {
		t.Fatalf("expected 33%% usage in JSON, got %+v", parsed.Accounts[0].Usage)
	}

	// Remove slot 1: non-TTY without --yes fails; with -y succeeds and renumbers slot 2 -> 1.
	rc, _, errOut := runCmd(env, "", false, "x", "remove", "1")
	if rc != 1 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("expected remove without --yes to fail: rc=%d err=%q", rc, errOut)
	}
	rc, out, _ := runCmd(env, "", false, "x", "remove", "1", "-y")
	if rc != 0 || !strings.Contains(out, "Remaining accounts were renumbered.") {
		t.Fatalf("remove -y failed: rc=%d out=%q", rc, out)
	}
	rc, out, _ = runCmd(env, "", false, "x", "list", "--no-usage")
	if rc != 0 || !strings.Contains(out, "1: two@example.com") {
		t.Fatalf("expected slot 2 renumbered to 1, got:\n%s", out)
	}
}
