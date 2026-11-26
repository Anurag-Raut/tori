package provider

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/store"
)

type memKeyring struct {
	mu    sync.Mutex
	items map[string]string
}

func newMemKeyring() *memKeyring {
	return &memKeyring{items: map[string]string{}}
}

func (k *memKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.items[service+"\x00"+user]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (k *memKeyring) Set(service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.items[service+"\x00"+user] = secret
	return nil
}

func (k *memKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.items, service+"\x00"+user)
	return nil
}

func seedStateDB(t *testing.T, dbPath, access, refresh string, expiry int64, email, name string, extra map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	upsert := func(k, v string) {
		if _, err := conn.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if refresh != "" {
		upsert(agyKeyUnifiedOAuth, createUnifiedOAuthToken(access, refresh, expiry))
	}
	if email != "" || name != "" {
		raw, _ := json.Marshal(map[string]any{
			"name":   name,
			"email":  email,
			"apiKey": access,
		})
		upsert(agyKeyAuthStatus, string(raw))
	}
	for k, v := range extra {
		upsert(k, v)
	}
}

func readStateDBKey(t *testing.T, dbPath, key string) (string, bool) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var val string
	err = conn.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return val, true
}

func TestAntigravityProtoRoundTrip(t *testing.T) {
	enc := createUnifiedOAuthToken("ya29.access-token-123", "1//0refresh-token-456", 1795584026)
	at, rt, exp, err := extractOAuthFromUnified(enc)
	if err != nil {
		t.Fatalf("extractOAuthFromUnified failed: %v", err)
	}
	if at != "ya29.access-token-123" || rt != "1//0refresh-token-456" || exp != 1795584026 {
		t.Fatalf("round-trip mismatch: at=%q rt=%q exp=%d", at, rt, exp)
	}
}

func TestAntigravityNotInstalledAndLoggedOutMessages(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	g := &Antigravity{}

	// Nothing installed on disk or keyring.
	st, err := g.Status(env)
	if err != nil {
		t.Fatal(err)
	}
	if st.LoggedIn || !strings.Contains(st.Message, "not found") || !strings.Contains(st.Message, "Desktop/IDE") || !strings.Contains(st.Message, "agy") {
		t.Fatalf("unexpected not-installed status: %+v", st)
	}
	if _, err := g.Add(env, nil); err == nil || !strings.Contains(err.Error(), "no Antigravity installation found") {
		t.Fatalf("expected not-installed Add error, got: %v", err)
	}

	// Create an empty Antigravity Desktop state.vscdb (installed, not logged in).
	dbPath := filepath.Join(env.Home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	seedStateDB(t, dbPath, "", "", 0, "", "", map[string]string{"workbench.colorTheme": "Dark+"})

	st, err = g.Status(env)
	if err != nil {
		t.Fatal(err)
	}
	if st.LoggedIn || st.Message != "Not logged in to Antigravity." {
		t.Fatalf("unexpected logged-out status: %+v", st)
	}
	if _, err := g.Add(env, nil); err == nil || !strings.Contains(err.Error(), "no Antigravity login found") {
		t.Fatalf("expected logged-out Add error, got: %v", err)
	}
}

func TestAntigravityDesktopStateDBLifecycleWithoutT3(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case agyGoogleTokenURL:
			return jsonResponse(200, map[string]any{"access_token": "refreshed-at", "expires_in": 3600}), nil
		case agyDailyEndpoint + "/v1internal:retrieveUserQuotaSummary":
			return jsonResponse(200, map[string]any{
				"groups": []any{
					map[string]any{
						"buckets": []any{
							map[string]any{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.8, "resetTime": "2026-11-26T17:00:00Z"},
							map[string]any{"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 0.6, "resetTime": "2026-12-01T00:00:00Z"},
						},
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

	dbPath := filepath.Join(env.Home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	freshExp := env.now().Unix() + 3600
	expiredExp := env.now().Unix() - 100

	// Account 1 (fresh access token, no T3 Code at all).
	seedStateDB(t, dbPath, "ya29.alice", "1//0rt-alice", freshExp, "alice@example.com", "Alice", map[string]string{
		"workbench.colorTheme": "Default Dark+",
		agyKeyLegacyAuth:       "stale-legacy",
	})

	st, err := g.Status(env)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn || st.DisplayName != "alice@example.com" || st.ManagedSlot != nil {
		t.Fatalf("unexpected unsaved desktop status: %+v", st)
	}
	out, err := g.Add(env, []string{"Work"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "Added slot 1: alice@example.com [Work]") {
		t.Fatalf("unexpected Add 1 output: %v", out)
	}

	// Account 2 (expired access token, will need OAuth refresh when fetching quota).
	seedStateDB(t, dbPath, "ya29.bob-expired", "1//0rt-bob", expiredExp, "bob@example.com", "Bob", nil)
	out, err = g.Add(env, []string{"Personal"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "Added slot 2: bob@example.com [Personal]") {
		t.Fatalf("unexpected Add 2 output: %v", out)
	}

	// Running process guard prevents mutating state.vscdb while Antigravity Desktop is open.
	env.ProcessRunning = func(surface string) bool { return surface == agySurfaceDesktop }
	if _, err := Switch(env, g, "1"); err == nil || !strings.Contains(err.Error(), "Antigravity Desktop is running") {
		t.Fatalf("expected running-app error, got: %v", err)
	}
	// Verify DB was NOT modified when blocked.
	if raw, ok := readStateDBKey(t, dbPath, agyKeyUnifiedOAuth); !ok {
		t.Fatal("missing oauth key")
	} else if _, rtVal, _, err := extractOAuthFromUnified(raw); err != nil || rtVal != "1//0rt-bob" {
		t.Fatalf("expected DB untouched on bob, got rt=%q err=%v", rtVal, err)
	}

	// Close Desktop -> switch to slot 1 succeeds and preserves unrelated settings.
	env.ProcessRunning = func(string) bool { return false }
	swOut, err := Switch(env, g, "1")
	if err != nil {
		t.Fatalf("Switch to slot 1 failed: %v", err)
	}
	if !strings.Contains(strings.Join(swOut, "\n"), "Open Antigravity Desktop") {
		t.Fatalf("expected desktop restart hint, got: %v", swOut)
	}
	if raw, ok := readStateDBKey(t, dbPath, agyKeyUnifiedOAuth); !ok {
		t.Fatal("missing oauth key after switch")
	} else if _, rtVal, _, err := extractOAuthFromUnified(raw); err != nil || rtVal != "1//0rt-alice" {
		t.Fatalf("expected alice token in DB, got rt=%q err=%v", rtVal, err)
	}
	if theme, ok := readStateDBKey(t, dbPath, "workbench.colorTheme"); !ok || theme != "Default Dark+" {
		t.Fatalf("expected unrelated IDE setting preserved, got %q ok=%v", theme, ok)
	}
	if _, hasLegacy := readStateDBKey(t, dbPath, agyKeyLegacyAuth); hasLegacy {
		t.Fatal("expected google.antigravity deleted on switch")
	}
	if onboard, ok := readStateDBKey(t, dbPath, agyKeyOnboarding); !ok || onboard != "true" {
		t.Fatalf("expected antigravityOnboarding=true, got %q ok=%v", onboard, ok)
	}

	// Quota fetch: slot 1 uses fresh embedded access token; slot 2 refreshes using client credentials.
	envMap["TORI_AGY_CLIENT_ID"] = "test-cid"
	envMap["TORI_AGY_CLIENT_SECRET"] = "test-csec"
	if err := config.SetUsage(env.Root, []string{"antigravity"}, true); err != nil {
		t.Fatal(err)
	}
	entries, cur, err := Entries(env, g, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || *cur != 1 || len(entries) != 2 {
		t.Fatalf("unexpected entries: cur=%v len=%d", cur, len(entries))
	}
	for i, e := range entries {
		if e.Usage == nil || len(e.Usage.Windows) != 2 || e.Usage.Windows[0].Pct != 20.0 || e.Usage.Windows[1].Pct != 40.0 {
			t.Fatalf("slot %d unexpected usage: %+v", i+1, e.Usage)
		}
	}

	// ClearLive removes auth keys while keeping unrelated settings, and restore brings login back.
	restore, err := g.ClearLive(env)
	if err != nil {
		t.Fatalf("ClearLive failed: %v", err)
	}
	if st, _ := g.Status(env); st.LoggedIn {
		t.Fatalf("expected logged out after ClearLive, got %+v", st)
	}
	if theme, ok := readStateDBKey(t, dbPath, "workbench.colorTheme"); !ok || theme != "Default Dark+" {
		t.Fatalf("expected theme preserved during ClearLive, got %q ok=%v", theme, ok)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if st, _ := g.Status(env); !st.LoggedIn || st.ManagedSlot == nil || *st.ManagedSlot != 1 {
		t.Fatalf("expected restored to slot 1, got %+v", st)
	}
}

func TestAntigravityIDEStateDBAndDarwinPaths(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	env.GOOS = "darwin"
	g := &Antigravity{}

	dbPath := filepath.Join(env.Home, "Library", "Application Support", "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	exp := env.now().Unix() + 3600
	seedStateDB(t, dbPath, "ya29.ide1", "1//0rt-ide1", exp, "ide1@example.com", "IDE One", nil)
	if _, err := g.Add(env, []string{"IDE1"}); err != nil {
		t.Fatal(err)
	}
	seedStateDB(t, dbPath, "ya29.ide2", "1//0rt-ide2", exp, "ide2@example.com", "IDE Two", nil)
	if _, err := g.Add(env, []string{"IDE2"}); err != nil {
		t.Fatal(err)
	}

	env.ProcessRunning = func(surface string) bool { return surface == agySurfaceIDE }
	if _, err := Switch(env, g, "1"); err == nil || !strings.Contains(err.Error(), "Antigravity IDE is running") {
		t.Fatalf("expected IDE running error, got: %v", err)
	}

	env.ProcessRunning = func(string) bool { return false }
	out, err := Switch(env, g, "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(out, "\n"), "Open Antigravity IDE") {
		t.Fatalf("expected IDE restart hint, got: %v", out)
	}
	slots, _ := g.Slots(env)
	if cur := g.Current(env, slots); cur == nil || *cur != 1 {
		t.Fatalf("expected active slot 1, got %v", cur)
	}
}

func TestAntigravityCLIKeyringAndFileFallbackLifecycle(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	kr := newMemKeyring()
	env.Keyring = kr
	g := &Antigravity{}

	cliDir := filepath.Join(env.Home, ".gemini", "antigravity-cli")
	cliTokenFile := filepath.Join(cliDir, "antigravity-oauth-token")
	cliSettingsFile := filepath.Join(cliDir, "settings.json")
	desktop2TokenFile := filepath.Join(env.Home, ".gemini", "jetski-standalone-oauth-token")
	if err := os.MkdirAll(filepath.Join(env.Home, ".gemini", "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = store.WriteJSONAtomic(cliSettingsFile, map[string]any{"theme": "dark"}, 0o600, false)

	// Account 1: consumer account in keyring (email comes from id_token JWT).
	consumerDoc, _ := json.Marshal(map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.cli-consumer",
			"token_type":    "Bearer",
			"refresh_token": "1//0rt-cli-1",
			"expiry":        env.now().Add(time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method": "consumer",
		"id_token":    makeJWT(map[string]any{"email": "cli-consumer@example.com", "sub": "sub-1"}),
	})
	_ = kr.Set(agyKeyringService, agyKeyringUser, string(consumerDoc))

	if out, err := g.Add(env, []string{"Personal"}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out[0], "cli-consumer@example.com [Personal]") {
		t.Fatalf("unexpected CLI add 1 output: %v", out)
	}

	// Account 2: GCP account in keyring.
	gcpDoc, _ := json.Marshal(map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.cli-gcp",
			"token_type":    "Bearer",
			"refresh_token": "1//0rt-cli-2",
			"expiry":        env.now().Add(time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method":    "gcp",
		"project_id":     "my-gcp-proj",
		"region":         "us",
		"id_token_email": "cli-gcp@example.com",
	})
	_ = kr.Set(agyKeyringService, agyKeyringUser, string(gcpDoc))

	if out, err := g.Add(env, []string{"WorkGCP"}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out[0], "cli-gcp@example.com [WorkGCP]") {
		t.Fatalf("unexpected CLI add 2 output: %v", out)
	}

	// Switch to slot 1 (consumer): writes keyring + CLI fallback + Desktop 2.x mirror, keeps theme, no gcp block.
	if _, err := Switch(env, g, "1"); err != nil {
		t.Fatal(err)
	}
	gotKR, err := kr.Get(agyKeyringService, agyKeyringUser)
	if err != nil || !strings.Contains(gotKR, "1//0rt-cli-1") {
		t.Fatalf("expected consumer token in keyring, got %q err=%v", gotKR, err)
	}
	if b, err := os.ReadFile(cliTokenFile); err != nil || !strings.Contains(string(b), "1//0rt-cli-1") {
		t.Fatalf("expected CLI fallback file mirrored, got %q err=%v", string(b), err)
	}
	if b, err := os.ReadFile(desktop2TokenFile); err != nil || !strings.Contains(string(b), "1//0rt-cli-1") {
		t.Fatalf("expected Desktop 2.x fallback file mirrored, got %q err=%v", string(b), err)
	}

	// Switch to slot 2 (GCP): adds gcp block to settings.json while preserving theme.
	if _, err := Switch(env, g, "2"); err != nil {
		t.Fatal(err)
	}
	settings := store.ReadJSONMap(cliSettingsFile)
	if settings["theme"] != "dark" || getString(getMap(settings, "gcp"), "project") != "my-gcp-proj" || getString(getMap(settings, "gcp"), "location") != "us" {
		t.Fatalf("expected gcp settings synced and theme preserved, got %+v", settings)
	}

	// Switch back to consumer (with keyring disabled -> file-only fallback still works!): removes gcp block.
	env.Keyring = nil
	if _, err := Switch(env, g, "1"); err != nil {
		t.Fatalf("file-only fallback switch failed: %v", err)
	}
	settings = store.ReadJSONMap(cliSettingsFile)
	if settings["theme"] != "dark" || settings["gcp"] != nil {
		t.Fatalf("expected gcp block removed on consumer switch, got %+v", settings)
	}
}

func TestAntigravityT3OAuthTokenAndLegacySlotCompat(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	g := &Antigravity{}

	// 1. Legacy v0.1/v0.2 slot containing ONLY acp_token.json (no auth.json).
	legacyDir := store.SlotDir(env.Root, g.Key(), 1)
	_ = store.WriteJSONAtomic(filepath.Join(legacyDir, agyLegacyFile), map[string]any{
		"client_id":     "cid",
		"client_secret": "csec",
		"refresh_token": "1//0rt-legacy",
		"project_id":    "proj-legacy",
	}, 0o600, false)
	_ = store.WriteJSONAtomic(filepath.Join(legacyDir, "meta.json"), store.Meta{
		Email: "legacy@example.com",
		Alias: "Legacy",
	}, 0o600, false)

	// 2. Newer T3 runtime using antigravity-oauth-token under ~/.t3/.../antigravity-acp/.
	t3Dir := filepath.Join(env.Home, ".t3", "userdata", "providers", "antigravity", "abc123", "antigravity-acp")
	t3OAuthPath := filepath.Join(t3Dir, "antigravity-oauth-token")
	_ = store.WriteJSONAtomic(t3OAuthPath, map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.t3-new",
			"token_type":    "Bearer",
			"refresh_token": "1//0rt-t3-new",
			"expiry":        env.now().Add(time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method": "consumer",
		"id_token":    makeJWT(map[string]any{"email": "t3new@example.com"}),
	}, 0o600, false)

	slots, err := g.Slots(env)
	if err != nil || len(slots) != 1 || slots[0].Meta.Email != "legacy@example.com" {
		t.Fatalf("expected legacy slot loaded, got %+v err=%v", slots, err)
	}

	// Add the new T3 antigravity-oauth-token as slot 2.
	if out, err := g.Add(env, []string{"NewT3"}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out[0], "Added slot 2: t3new@example.com [NewT3]") {
		t.Fatalf("unexpected T3 oauth add output: %v", out)
	}

	// Switch to legacy slot 1 -> writes acp_token.json in T3 dir.
	if _, err := Switch(env, g, "1"); err != nil {
		t.Fatalf("switch to legacy slot 1 failed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(t3Dir, "acp_token.json")); err != nil || !strings.Contains(string(b), "1//0rt-legacy") {
		t.Fatalf("expected legacy acp_token.json written, got %q err=%v", string(b), err)
	}

	// Remove slot 1 -> slot 2 compacts cleanly to slot 1!
	slots, _ = g.Slots(env)
	if _, err := Remove(env, g, slots[0]); err != nil {
		t.Fatalf("Remove legacy slot failed: %v", err)
	}
	slots, _ = g.Slots(env)
	if len(slots) != 1 || slots[0].Number != 1 || slots[0].Meta.Email != "t3new@example.com" {
		t.Fatalf("expected slot 2 compacted to slot 1, got %+v", slots)
	}
}

func TestAntigravityMultiSurfaceMergeAndIsolation(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	kr := newMemKeyring()
	env.Keyring = kr
	g := &Antigravity{}

	dbPath := filepath.Join(env.Home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	t3Path := filepath.Join(env.Home, ".t3", "userdata", "providers", "antigravity", "hash1", "antigravity-acp", "acp_token.json")
	exp := env.now().Unix() + 3600

	// Desktop signed in as alice@example.com -> saved as slot 1.
	seedStateDB(t, dbPath, "ya29.desk-a", "1//0rt-desk-a", exp, "alice@example.com", "Alice", nil)
	if _, err := g.Add(env, []string{"Alice"}); err != nil {
		t.Fatal(err)
	}

	// Now CLI also signs in as alice@example.com -> Add merges CLI into existing slot 1!
	cliDoc, _ := json.Marshal(map[string]any{
		"token": map[string]any{
			"access_token":  "ya29.cli-a",
			"token_type":    "Bearer",
			"refresh_token": "1//0rt-cli-a",
			"expiry":        env.now().Add(time.Hour).Format(time.RFC3339Nano),
		},
		"auth_method": "consumer",
		"id_token":    makeJWT(map[string]any{"email": "alice@example.com"}),
	})
	_ = kr.Set(agyKeyringService, agyKeyringUser, string(cliDoc))
	out, err := g.Add(env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[0], "Updated slot 1") || !strings.Contains(out[0], "Antigravity CLI") {
		t.Fatalf("expected CLI merged into slot 1, got %v", out)
	}

	// Meanwhile T3 Code is signed in as an unrelated account (not captured in slot 1).
	_ = store.WriteJSONAtomic(t3Path, map[string]any{"refresh_token": "1//0rt-t3-other"}, 0o600, false)

	// Switch Desktop ONLY to bob@example.com (unsaved), while CLI stays on alice@example.com.
	seedStateDB(t, dbPath, "ya29.desk-b", "1//0rt-desk-b", exp, "bob@example.com", "Bob", nil)
	// Touch dbPath slightly newer than t3Path so Desktop is clearly the newest unsaved login.
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(dbPath, future, future)

	if st, err := g.Status(env); err != nil || !st.LoggedIn || st.ManagedSlot != nil || st.DisplayName != "bob@example.com" {
		t.Fatalf("expected unsaved bob@example.com status, got %+v err=%v", st, err)
	}
	if out, err := g.Add(env, []string{"Bob"}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(out[0], "Added slot 2: bob@example.com [Bob]") {
		t.Fatalf("unexpected slot 2 add: %v", out)
	}

	// Switching to slot 1 switches Desktop AND CLI, and does NOT touch T3 Code!
	if _, err := Switch(env, g, "1"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := readStateDBKey(t, dbPath, agyKeyUnifiedOAuth); raw == "" {
		t.Fatal("missing desktop oauth after switch")
	} else if _, rt, _, _ := extractOAuthFromUnified(raw); rt != "1//0rt-desk-a" {
		t.Fatalf("expected desktop switched to alice, got %q", rt)
	}
	if gotKR, _ := kr.Get(agyKeyringService, agyKeyringUser); !strings.Contains(gotKR, "1//0rt-cli-a") {
		t.Fatalf("expected CLI switched to alice, got %q", gotKR)
	}
	if t3Bytes, _ := os.ReadFile(t3Path); !strings.Contains(string(t3Bytes), "1//0rt-t3-other") {
		t.Fatalf("expected T3 Code untouched by Desktop+CLI slot, got %q", string(t3Bytes))
	}
}

func TestMatchAntigravityProcess(t *testing.T) {
	cases := []struct {
		cmdline string
		surface string
		want    bool
	}{
		{"/usr/lib/electron39/electron /usr/lib/antigravity/out/cli.js --app=/usr/lib/antigravity/", "desktop", true},
		{"/usr/lib/electron39/electron /usr/lib/antigravity/out/cli.js --app=/usr/lib/antigravity/", "ide", false},
		{"/Applications/Antigravity.app/Contents/MacOS/Antigravity", "desktop", true},
		{"/Applications/Antigravity IDE.app/Contents/MacOS/Electron", "ide", true},
		{"/Applications/Antigravity IDE.app/Contents/MacOS/Electron", "desktop", false},
		{"/usr/bin/antigravity-ide", "ide", true},
		// Must NOT false-positive on T3 Code ACP harness, CLI, or tori!
		{"/home/user/.t3/tools/antigravity-acp/linux-x64/agy_acp_server.par --uid=", "desktop", false},
		{"/home/user/.t3/tools/antigravity-acp/linux-x64/localharness_external", "desktop", false},
		{"/home/user/.local/bin/agy chat", "desktop", false},
		{"tori g switch 1", "desktop", false},
		{"/opt/Antigravity Manager/antigravity-manager", "desktop", false},
	}
	for _, tc := range cases {
		if got := matchAntigravityProcess(tc.cmdline, tc.surface); got != tc.want {
			t.Errorf("matchAntigravityProcess(%q, %q) = %v, want %v", tc.cmdline, tc.surface, got, tc.want)
		}
	}
}

func TestResolveAgyWrapper(t *testing.T) {
	dir := t.TempDir()
	realBin := filepath.Join(dir, "agy-real")
	if err := os.WriteFile(realBin, []byte("binary-payload"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "agy")
	script := "#!/bin/sh\nexec \"" + realBin + "\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgyWrapper(wrapper); got != realBin {
		t.Fatalf("resolveAgyWrapper = %q, want %q", got, realBin)
	}
}

func TestCollectAgyCandidatesAndDiscovery(t *testing.T) {
	// Verify candidate extraction handles non-null-terminated binary string tables.
	fakeID := "123456789012-" + strings.Repeat("a", 32) + agyClientIDSuffix
	fakeSec := agyClientSecretPrefix + strings.Repeat("x", 28)
	payload := []byte("prefix-noise" + fakeID + "TRAILING_GARBAGE" + fakeSec + "MORE_GARBAGE")
	ids := map[string]bool{}
	secrets := map[string]bool{}
	collectAgyCandidates(payload, ids, secrets)

	id, sec := matchAgyClientPair(ids, secrets, sha256Hex(fakeID), sha256Hex(fakeSec))
	if id != fakeID || sec != fakeSec {
		t.Fatalf("matchAgyClientPair failed: got id=%v sec=%v", id != "", sec != "")
	}

	// If Antigravity Desktop is installed on this host, verify hash-matched discovery works.
	const realLS = "/usr/lib/antigravity/extensions/antigravity/bin/language_server_linux_x64"
	if _, err := os.Stat(realLS); err == nil {
		creds, ok := scanAgyBinaryCached(realLS)
		if !ok || sha256Hex(creds.clientID) != agyClientIDHash {
			t.Fatalf("expected hash-matched OAuth client from installed language_server, ok=%v", ok)
		}
		secHash := sha256Hex(creds.clientSecret)
		if secHash != agyClientSecretHashV1 && secHash != agyClientSecretHashV2 {
			t.Fatalf("unexpected OAuth secret hash from installed language_server")
		}
	}
}
