package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anurag-Raut/tori/internal/store"
)

type failingKeyring struct {
	err error
}

func (f failingKeyring) Get(string, string) (string, error)   { return "", f.err }
func (f failingKeyring) Set(string, string, string) error     { return f.err }
func (f failingKeyring) Delete(string, string) error          { return f.err }

func TestKeychainSecretDecodingAndCommandFormatting(t *testing.T) {
	plain := `{"claudeAiOauth":{"accessToken":"at-1"}}`
	if got := decodeKeychainSecret(plain + "\n"); got != plain {
		t.Fatalf("plain decode mismatch: got %q", got)
	}
	hexJSON := hex.EncodeToString([]byte("{\n  \"k\": \"v\"\n}"))
	if got := decodeKeychainSecret(hexJSON + "\n"); got != "{\n  \"k\": \"v\"\n}" {
		t.Fatalf("hex JSON decode mismatch: got %q", got)
	}
	b64Legacy := goKeyringBase64Prefix + base64.StdEncoding.EncodeToString([]byte(plain))
	if got := decodeKeychainSecret(b64Legacy); got != plain {
		t.Fatalf("legacy base64 decode mismatch: got %q", got)
	}
	hexLegacy := goKeyringHexPrefix + hex.EncodeToString([]byte(plain))
	if got := decodeKeychainSecret(hexLegacy); got != plain {
		t.Fatalf("legacy hex decode mismatch: got %q", got)
	}

	cmd := buildSecurityAddCommand("Claude Code-credentials", "dev", plain)
	if !strings.Contains(cmd, `add-generic-password -U -a "dev" -s "Claude Code-credentials" -X `) {
		t.Fatalf("unexpected security command: %q", cmd)
	}
	if !strings.Contains(cmd, hex.EncodeToString([]byte(plain))) {
		t.Fatalf("expected hex payload in security command: %q", cmd)
	}
}

func TestClaudeInstalledVsNotInstalledAndLockedKeychain(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	c := &Claude{}

	// 1. Not installed at all.
	st, err := c.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "not installed") {
		t.Fatalf("expected Claude not-installed status, got %+v err=%v", st, err)
	}
	if _, err := c.Add(env, nil); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected Claude not-installed Add error, got %v", err)
	}

	// 2. Installed (binary on PATH), but not logged in.
	env.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/local/bin/claude", nil
		}
		return "", errors.New("not found")
	}
	st, err = c.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "installed, but not logged in") {
		t.Fatalf("expected Claude installed-not-logged-in status, got %+v err=%v", st, err)
	}
	if _, err := c.Add(env, nil); err == nil || !strings.Contains(err.Error(), "not logged in to Claude Code") {
		t.Fatalf("expected Claude not-logged-in Add error, got %v", err)
	}

	// 3. macOS locked Keychain error surfaces a helpful Keychain hint.
	env.GOOS = "darwin"
	envMap["USER"] = "dev"
	env.Keyring = failingKeyring{err: errors.New("user interaction is not allowed")}
	if _, err := c.Add(env, nil); err == nil || !strings.Contains(err.Error(), "Keychain") {
		t.Fatalf("expected macOS locked Keychain hint, got %v", err)
	}
}

func TestClaudeMacOSKeychainLifecycleAndCustomConfigDir(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	env.GOOS = "darwin"
	kr := newMemKeyring()
	env.Keyring = kr
	envMap["USER"] = "dev"
	c := &Claude{}

	cfgPath := filepath.Join(env.Home, ".claude.json")
	writeKC := func(service, user, uuid, email, token, plan string, extra map[string]any) {
		payload := map[string]any{
			"claudeAiOauth": map[string]any{
				"accessToken":      token,
				"refreshToken":     "rt-" + uuid,
				"expiresAt":        float64(env.now().Unix()+3600) * 1000,
				"subscriptionType": plan,
			},
		}
		for k, v := range extra {
			payload[k] = v
		}
		raw, _ := json.Marshal(payload)
		_ = kr.Set(service, user, string(raw))
		_ = store.WriteJSONAtomic(cfgPath, map[string]any{
			"theme": "dark",
			"oauthAccount": map[string]any{
				"accountUuid":  uuid,
				"emailAddress": email,
			},
		}, 0o600, false)
	}

	// Account 1 in macOS Keychain with sibling mcpOAuth preserved.
	writeKC("Claude Code-credentials", "dev", "uuid-1", "alice@example.com", "at-1", "pro", map[string]any{
		"mcpOAuth": map[string]any{"srv": "keep-me"},
	})

	st, err := c.Status(env)
	if err != nil || !st.LoggedIn || st.DisplayName != "alice@example.com" || st.Plan != "Pro" {
		t.Fatalf("unexpected darwin Keychain status: %+v err=%v", st, err)
	}
	if out, err := c.Add(env, []string{"Work"}); err != nil || !strings.Contains(out[0], "Added slot 1: alice@example.com [Work] · Pro") {
		t.Fatalf("unexpected darwin Add 1: %v err=%v", out, err)
	}

	// Account 2 in macOS Keychain.
	writeKC("Claude Code-credentials", "dev", "uuid-2", "bob@example.com", "at-2", "max", map[string]any{
		"mcpOAuth": map[string]any{"srv": "keep-me"},
	})
	if out, err := c.Add(env, []string{"Personal"}); err != nil || !strings.Contains(out[0], "Added slot 2: bob@example.com [Personal] · Max") {
		t.Fatalf("unexpected darwin Add 2: %v err=%v", out, err)
	}

	// Switch back to slot 1: Keychain updated with raw JSON (no plaintext file created), mcpOAuth preserved.
	if _, err := Switch(env, c, "1"); err != nil {
		t.Fatalf("Switch to slot 1 failed: %v", err)
	}
	rawKC, err := kr.Get("Claude Code-credentials", "dev")
	if err != nil {
		t.Fatalf("expected Keychain entry after switch: %v", err)
	}
	var kcMap map[string]any
	if err := json.Unmarshal([]byte(rawKC), &kcMap); err != nil {
		t.Fatalf("Keychain entry not valid JSON: %v", err)
	}
	if getString(getMap(kcMap, "claudeAiOauth"), "accessToken") != "at-1" {
		t.Fatalf("expected at-1 in Keychain after switch, got %v", kcMap)
	}
	if getString(getMap(kcMap, "mcpOAuth"), "srv") != "keep-me" {
		t.Fatalf("expected mcpOAuth preserved in Keychain, got %v", kcMap)
	}
	if fileExists(filepath.Join(env.Home, ".claude", ".credentials.json")) {
		t.Fatal("should not create plaintext .credentials.json when Keychain write succeeded")
	}

	// ClearLive clears claudeAiOauth from Keychain while preserving mcpOAuth, and restore brings it back.
	restore, err := c.ClearLive(env)
	if err != nil {
		t.Fatalf("ClearLive failed: %v", err)
	}
	if st, _ := c.Status(env); st.LoggedIn {
		t.Fatalf("expected logged out after ClearLive, got %+v", st)
	}
	rawAfterClear, _ := kr.Get("Claude Code-credentials", "dev")
	if !strings.Contains(rawAfterClear, "keep-me") || strings.Contains(rawAfterClear, "claudeAiOauth") {
		t.Fatalf("expected mcpOAuth preserved and claudeAiOauth removed, got %q", rawAfterClear)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if st, _ := c.Status(env); !st.LoggedIn || st.DisplayName != "alice@example.com" {
		t.Fatalf("expected restored login, got %+v", st)
	}

	// Custom CLAUDE_CONFIG_DIR + username with invalid chars falls back to hashed service and "claude-code-user".
	customDir := filepath.Join(env.Home, "alt-claude")
	_ = os.MkdirAll(customDir, 0o700)
	envMap["CLAUDE_CONFIG_DIR"] = customDir
	envMap["USER"] = "user@sso.example.com"
	sum := sha256.Sum256([]byte(customDir))
	wantSvc := "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
	if got := c.keychainService(env); got != wantSvc {
		t.Fatalf("keychainService = %q, want %q", got, wantSvc)
	}
	cfgPath = filepath.Join(customDir, ".claude.json")
	writeKC(wantSvc, "claude-code-user", "uuid-3", "sso@example.com", "at-3", "pro", nil)
	if st, err := c.Status(env); err != nil || !st.LoggedIn || st.DisplayName != "sso@example.com" {
		t.Fatalf("expected SSO fallback keychain login, got %+v err=%v", st, err)
	}
}

func TestCodexInstalledVsNotInstalledAndEphemeral(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	x := &Codex{}

	// 1. Not installed.
	st, err := x.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "not installed") {
		t.Fatalf("expected Codex not-installed status, got %+v err=%v", st, err)
	}
	if _, err := x.Add(env, nil); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected Codex not-installed Add error, got %v", err)
	}

	// 2. Installed (binary on PATH), not logged in.
	env.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return "/usr/local/bin/codex", nil
		}
		return "", errors.New("not found")
	}
	st, err = x.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "installed, but not logged in") {
		t.Fatalf("expected Codex installed-not-logged-in status, got %+v err=%v", st, err)
	}
	if _, err := x.Add(env, nil); err == nil || !strings.Contains(err.Error(), "not logged in to Codex") {
		t.Fatalf("expected Codex not-logged-in Add error, got %v", err)
	}

	// 3. Configured with ephemeral store.
	codexDir := filepath.Join(env.Home, ".codex")
	_ = os.MkdirAll(codexDir, 0o700)
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"ephemeral\"\n"), 0o600)
	st, err = x.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "ephemeral") {
		t.Fatalf("expected ephemeral status message, got %+v err=%v", st, err)
	}
	if _, err := x.Add(env, nil); err == nil || !strings.Contains(err.Error(), "ephemeral") {
		t.Fatalf("expected ephemeral Add error, got %v", err)
	}
}

func TestCodexConfigTOMLParsingAndKeyringLifecycle(t *testing.T) {
	tomlCases := []struct {
		text string
		want string
	}{
		{`cli_auth_credentials_store = "keyring"`, "keyring"},
		{`# comment` + "\n" + `cli_auth_credentials_store = 'auto' # inline`, "auto"},
		{"[mcp_servers.demo]\ncli_auth_credentials_store = \"keyring\"\n", "file"},
		{"model = \"o3\"\n", "file"},
	}
	for _, tc := range tomlCases {
		if got := parseCodexCredStoreMode(tc.text); got != tc.want {
			t.Fatalf("parseCodexCredStoreMode(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}

	env, _ := newTestEnv(t, nil)
	kr := newMemKeyring()
	env.Keyring = kr
	x := &Codex{}

	codexDir := filepath.Join(env.Home, ".codex")
	_ = os.MkdirAll(codexDir, 0o700)
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"keyring\"\n"), 0o600)

	acctKey := x.keyringAccount(env)
	if !strings.HasPrefix(acctKey, "cli|") || len(acctKey) != len("cli|")+16 {
		t.Fatalf("unexpected Codex keyring account key: %q", acctKey)
	}

	seedKR := func(id, email string) {
		payload := map[string]any{
			"auth_mode": "chatgpt",
			"tokens": map[string]any{
				"access_token":  makeJWT(map[string]any{"exp": env.now().Unix() + 3600}),
				"refresh_token": "rt-" + id,
				"account_id":    id,
				"id_token": makeJWT(map[string]any{
					"email":                       email,
					"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
				}),
			},
		}
		raw, _ := json.Marshal(payload)
		_ = kr.Set(codexKeyringService, acctKey, string(raw))
	}

	seedKR("acct-1", "one@example.com")
	if out, err := x.Add(env, []string{"Work"}); err != nil || !strings.Contains(out[0], "Added slot 1: one@example.com [Work] · Plus") {
		t.Fatalf("Add 1 failed: %v err=%v", out, err)
	}

	seedKR("acct-2", "two@example.com")
	if out, err := x.Add(env, []string{"Personal"}); err != nil || !strings.Contains(out[0], "Added slot 2: two@example.com [Personal] · Plus") {
		t.Fatalf("Add 2 failed: %v err=%v", out, err)
	}

	// Leave a stale auth.json to verify keyring-mode Switch cleans it up (matching Codex CLI).
	staleAuth := filepath.Join(codexDir, "auth.json")
	_ = os.WriteFile(staleAuth, []byte(`{"stale":true}`), 0o600)

	if _, err := Switch(env, x, "1"); err != nil {
		t.Fatalf("Switch to slot 1 failed: %v", err)
	}
	if fileExists(staleAuth) {
		t.Fatal("expected stale auth.json removed in keyring mode")
	}
	if st, err := x.Status(env); err != nil || !st.LoggedIn || st.DisplayName != "one@example.com" || st.ManagedSlot == nil || *st.ManagedSlot != 1 {
		t.Fatalf("unexpected status after keyring switch: %+v err=%v", st, err)
	}

	// ClearLive & restore round-trip on keyring.
	restore, err := x.ClearLive(env)
	if err != nil {
		t.Fatalf("ClearLive failed: %v", err)
	}
	if st, _ := x.Status(env); st.LoggedIn {
		t.Fatalf("expected logged out after ClearLive, got %+v", st)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if st, _ := x.Status(env); !st.LoggedIn || st.DisplayName != "one@example.com" {
		t.Fatalf("expected restored keyring login, got %+v", st)
	}

	// Auto mode falls back to auth.json when keyring is unavailable.
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"auto\"\n"), 0o600)
	env.Keyring = failingKeyring{err: errors.New("no secret service")}
	if _, err := Switch(env, x, "2"); err != nil {
		t.Fatalf("auto-mode fallback switch failed: %v", err)
	}
	if !fileExists(staleAuth) {
		t.Fatal("expected auth.json written as fallback in auto mode")
	}
	if st, err := x.Status(env); err != nil || !st.LoggedIn || st.DisplayName != "two@example.com" {
		t.Fatalf("unexpected status after auto fallback: %+v err=%v", st, err)
	}
}
