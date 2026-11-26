package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anurag-Raut/tori/internal/store"
	"github.com/zalando/go-keyring"
)

func TestWindowsKeyringHelpersAndTargets(t *testing.T) {
	codexTargets := windowsCredTargetNames(codexKeyringService, "cli|0123456789abcdef")
	if len(codexTargets) != 2 || codexTargets[0] != "cli|0123456789abcdef.Codex Auth" || codexTargets[1] != "Codex Auth:cli|0123456789abcdef" {
		t.Fatalf("unexpected Codex Windows targets: %v", codexTargets)
	}
	agyTargets := windowsCredTargetNames(agyKeyringService, agyKeyringUser)
	if len(agyTargets) != 2 || agyTargets[0] != "gemini:antigravity" || agyTargets[1] != "antigravity.gemini" {
		t.Fatalf("unexpected Antigravity Windows targets: %v", agyTargets)
	}

	// Plain UTF-8 JSON blob.
	plain := `{"token":{"refresh_token":"1//0rt-win"}}`
	if got := decodeWindowsCredBlob([]byte(plain)); got != plain {
		t.Fatalf("UTF-8 decode mismatch: got %q", got)
	}

	// Rust-style target must encode as UTF-16LE (including non-BMP surrogate pairs).
	withEmoji := `{"token":{"label":"work 🚀","refresh_token":"1//0rt-win"}}`
	rustBlob := encodeWindowsCredBlob(codexTargets[0], codexKeyringService, "cli|0123456789abcdef", withEmoji, nil)
	if !isWindowsUTF16LEBlob(rustBlob) {
		t.Fatalf("expected UTF-16LE encoding for Rust-style target, got %x", rustBlob)
	}
	if got := decodeWindowsCredBlob(rustBlob); got != withEmoji {
		t.Fatalf("UTF-16LE round-trip mismatch: got %q, want %q", got, withEmoji)
	}

	// Go-style target encodes as UTF-8 by default, and preserves UTF-16LE if existing entry was UTF-16LE.
	goUTF8 := encodeWindowsCredBlob(agyTargets[0], agyKeyringService, agyKeyringUser, plain, nil)
	if string(goUTF8) != plain {
		t.Fatalf("expected UTF-8 encoding for Go-style target, got %q", string(goUTF8))
	}
	goMirrored := encodeWindowsCredBlob(agyTargets[0], agyKeyringService, agyKeyringUser, plain, rustBlob)
	if !isWindowsUTF16LEBlob(goMirrored) || decodeWindowsCredBlob(goMirrored) != plain {
		t.Fatalf("expected mirrored UTF-16LE encoding when existing blob is UTF-16LE")
	}

	// UTF-16LE with trailing NUL still decodes cleanly.
	withNul := append(encodeWindowsUTF16LE(plain), 0, 0)
	if got := decodeWindowsCredBlob(withNul); got != plain {
		t.Fatalf("UTF-16LE with NUL decode mismatch: got %q", got)
	}
}

func TestWindowsAntigravityPathsProcessMatchingAndDiscovery(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	env.GOOS = "windows"
	env.GOARCH = "amd64"
	appData := filepath.Join(env.Home, "AppData", "Roaming")
	localAppData := filepath.Join(env.Home, "AppData", "Local")
	envMap["APPDATA"] = appData
	envMap["LOCALAPPDATA"] = localAppData

	g := &Antigravity{}
	deskDB := filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb")
	ideDB := filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb")

	exp := env.now().Unix() + 3600
	seedStateDB(t, deskDB, "ya29.win-desk", "1//0rt-win-desk", exp, "windesk@example.com", "Win Desk", nil)

	gotDesk, instDesk := g.desktopDBPath(env)
	if !instDesk || gotDesk != deskDB {
		t.Fatalf("desktopDBPath = (%q, %v), want (%q, true)", gotDesk, instDesk, deskDB)
	}
	if out, err := g.Add(env, []string{"WinDesk"}); err != nil || !strings.Contains(out[0], "windesk@example.com [WinDesk]") {
		t.Fatalf("Windows Antigravity Desktop Add failed: %v err=%v", out, err)
	}

	seedStateDB(t, ideDB, "ya29.win-ide", "1//0rt-win-ide", exp, "winide@example.com", "Win IDE", nil)
	gotIDE, instIDE := g.ideDBPath(env)
	if !instIDE || gotIDE != ideDB {
		t.Fatalf("ideDBPath = (%q, %v), want (%q, true)", gotIDE, instIDE, ideDB)
	}
	if out, err := g.Add(env, []string{"WinIDE"}); err != nil || !strings.Contains(out[0], "winide@example.com [WinIDE]") {
		t.Fatalf("Windows Antigravity IDE Add failed: %v err=%v", out, err)
	}

	// Verify Windows process matching & CSV tasklist parser.
	name, pid, ok := parseWindowsTasklistCSVLine(`"Antigravity.exe","4120","Console","1","125,000 K"`)
	if !ok || name != "Antigravity.exe" || pid != 4120 {
		t.Fatalf("parseWindowsTasklistCSVLine = (%q, %d, %v)", name, pid, ok)
	}

	procCases := []struct {
		cmdline string
		surface string
		want    bool
	}{
		{`"C:\Users\dev\AppData\Local\Programs\Antigravity\Antigravity.exe" --type=renderer`, "desktop", true},
		{`"C:\Users\dev\AppData\Local\Programs\Antigravity\Antigravity.exe" --type=renderer`, "ide", false},
		{`"C:\Users\dev\AppData\Local\Programs\Antigravity IDE\Antigravity IDE.exe"`, "ide", true},
		{`"C:\Users\dev\AppData\Local\Programs\Antigravity IDE\Antigravity IDE.exe"`, "desktop", false},
		{`Antigravity.exe`, "desktop", true},
		{`"C:\Users\dev\.t3\tools\antigravity-acp\win32-x64\agy_acp_server.exe"`, "desktop", false},
		{`"C:\Users\dev\.local\bin\agy.exe" chat`, "desktop", false},
		{`"C:\Program Files\tori\tori.exe" g switch 1`, "desktop", false},
		{`powershell.exe -Command "Get-CimInstance Win32_Process | Where antigravity"`, "desktop", false},
	}
	for _, tc := range procCases {
		if got := matchAntigravityProcess(tc.cmdline, tc.surface); got != tc.want {
			t.Errorf("matchAntigravityProcess(%q, %q) = %v, want %v", tc.cmdline, tc.surface, got, tc.want)
		}
	}

	// Verify Windows language_server binary discovery path.
	fakeLS := filepath.Join(localAppData, "Programs", "Antigravity", "resources", "app", "extensions", "antigravity", "bin", "language_server_windows_x64.exe")
	if err := os.MkdirAll(filepath.Dir(fakeLS), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeLS, []byte("stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	bins := agyBinaryCandidates(env)
	found := false
	for _, b := range bins {
		if b == fakeLS {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Windows language_server %q in candidates %v", fakeLS, bins)
	}
}

func TestWindowsCodexVerbatimKeyringHash(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	env.GOOS = "windows"
	kr := newMemKeyring()
	env.Keyring = kr
	x := &Codex{}

	codexDir := filepath.Join(env.Home, ".codex")
	_ = os.MkdirAll(codexDir, 0o700)
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"keyring\"\n"), 0o600)

	// Compute Rust's Windows verbatim `\\?\` canonical hash.
	absDir, _ := filepath.Abs(codexDir)
	if resolved, err := filepath.EvalSymlinks(absDir); err == nil {
		absDir = resolved
	}
	winPath := `\\?\` + strings.ReplaceAll(absDir, "/", `\`)
	sum := sha256.Sum256([]byte(winPath))
	verbatimKey := "cli|" + hex.EncodeToString(sum[:])[:16]

	payload, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.now().Unix() + 3600}),
			"refresh_token": "rt-win-codex",
			"account_id":    "acct-win-1",
			"id_token": makeJWT(map[string]any{
				"email":                       "wincodex@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"},
			}),
		},
	})
	_ = kr.Set(codexKeyringService, verbatimKey, string(payload))

	st, err := x.Status(env)
	if err != nil || !st.LoggedIn || st.DisplayName != "wincodex@example.com" {
		t.Fatalf("expected Windows verbatim keyring Codex login, got %+v err=%v", st, err)
	}

	// Also verify Claude Code file-based storage on Windows works cleanly.
	c := &Claude{}
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "at-win-claude",
			"refreshToken":     "rt-win-claude",
			"expiresAt":        float64(env.now().Unix()+3600) * 1000,
			"subscriptionType": "pro",
		},
	}, 0o600, false)
	_ = store.WriteJSONAtomic(filepath.Join(env.Home, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{
			"accountUuid":  "uuid-win-1",
			"emailAddress": "winclaude@example.com",
		},
	}, 0o600, false)
	if st, err := c.Status(env); err != nil || !st.LoggedIn || st.DisplayName != "winclaude@example.com" {
		t.Fatalf("expected Windows Claude login, got %+v err=%v", st, err)
	}
}

type sizeLimitedKeyring struct {
	*memKeyring
	maxBytes int
}

func (s *sizeLimitedKeyring) Set(service, user, secret string) error {
	if len(secret) > s.maxBytes {
		return keyring.ErrSetDataTooBig
	}
	return s.memKeyring.Set(service, user, secret)
}

func TestCodexAutoModeClearsStaleKeyringAndClearLiveFallbackHash(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	env.GOOS = "windows"
	mem := newMemKeyring()
	env.Keyring = &sizeLimitedKeyring{memKeyring: mem, maxBytes: 200}
	x := &Codex{}

	codexDir := filepath.Join(env.Home, ".codex")
	_ = os.MkdirAll(codexDir, 0o700)
	_ = os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte("cli_auth_credentials_store = \"auto\"\n"), 0o600)

	// 1. Seed a small API-key login under the non-verbatim fallback hash (candidates[1]).
	candidates := x.keyringAccounts(env)
	if len(candidates) < 2 {
		t.Fatalf("expected verbatim + plain candidates on windows, got %v", candidates)
	}
	apiKeyJSON := `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-old-api-key"}`
	if err := mem.Set(codexKeyringService, candidates[1], apiKeyJSON); err != nil {
		t.Fatal(err)
	}

	// Verify ClearLive removes the non-verbatim fallback hash entry and restore brings it back.
	restore, err := x.ClearLive(env)
	if err != nil {
		t.Fatalf("ClearLive failed: %v", err)
	}
	if _, err := mem.Get(codexKeyringService, candidates[1]); err == nil {
		t.Fatalf("expected ClearLive to remove fallback keyring account %q", candidates[1])
	}
	if err := restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if got, err := mem.Get(codexKeyringService, candidates[1]); err != nil || got != apiKeyJSON {
		t.Fatalf("expected restore to restore fallback keyring account, got (%q, %v)", got, err)
	}

	// 2. Now switch in `auto` mode to a large OAuth login that exceeds the keyring size limit.
	//    Tori must write auth.json AND delete the stale API-key keyring entry so it won't shadow auth.json.
	largeOAuth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  makeJWT(map[string]any{"exp": env.now().Unix() + 3600, "pad": strings.Repeat("a", 300)}),
			"refresh_token": "rt-large-oauth",
			"account_id":    "acct-oauth-1",
			"id_token": makeJWT(map[string]any{
				"email":                       "oauth@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"},
			}),
		},
	}
	if err := x.applyLive(env, largeOAuth); err != nil {
		t.Fatalf("applyLive(auto) failed: %v", err)
	}
	for _, acct := range candidates {
		if got, err := mem.Get(codexKeyringService, acct); err == nil {
			t.Fatalf("expected stale keyring entry %q to be deleted on auto fallback, still found %q", acct, got)
		}
	}
	st, err := x.Status(env)
	if err != nil || !st.LoggedIn || st.DisplayName != "oauth@example.com" {
		t.Fatalf("expected OAuth login via auth.json after auto fallback, got %+v err=%v", st, err)
	}
}

