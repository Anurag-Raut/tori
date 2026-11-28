package provider

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/store"
)

type memKeyring struct {
	mu     sync.Mutex
	items  map[string]string
	gets   int
	writes int
}

func newMemKeyring() *memKeyring {
	return &memKeyring{items: map[string]string{}}
}

func (k *memKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gets++
	v, ok := k.items[service+"\x00"+user]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}

func (k *memKeyring) Set(service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writes++
	k.items[service+"\x00"+user] = secret
	return nil
}

func (k *memKeyring) Delete(service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writes++
	delete(k.items, service+"\x00"+user)
	return nil
}

type agyFixture struct {
	env     *Env
	envMap  map[string]string
	keyring *memKeyring
	bin     string
	ran     [][]string
	runErr  error
	clock   time.Time
}

// newAgyFixture returns an env with agy installed and a recording command runner.
func newAgyFixture(t *testing.T, rt http.RoundTripper) *agyFixture {
	t.Helper()
	env, envMap := newTestEnv(t, rt)
	f := &agyFixture{env: env, envMap: envMap, keyring: newMemKeyring(), clock: env.now()}
	f.bin = filepath.Join(env.Home, ".local", "bin", "agy")
	if err := store.WriteBytesAtomic(f.bin, []byte("stub"), 0o700, false); err != nil {
		t.Fatal(err)
	}
	env.Keyring = f.keyring
	env.Now = func() time.Time { return f.clock }
	env.RunCommand = func(cmd *exec.Cmd) error {
		f.ran = append(f.ran, cmd.Args)
		return f.runErr
	}
	return f
}

func (f *agyFixture) tick() { f.clock = f.clock.Add(time.Minute) }

func (f *agyFixture) current(t *testing.T) int {
	t.Helper()
	g := &Antigravity{}
	accts, err := g.Slots(f.env)
	if err != nil {
		t.Fatal(err)
	}
	if cur := g.Current(f.env, accts); cur != nil {
		return *cur
	}
	return 0
}

func (f *agyFixture) login(t *testing.T, name string) {
	t.Helper()
	f.tick()
	if _, err := (&Antigravity{}).Login(f.env, name); err != nil {
		t.Fatalf("login %q: %v", name, err)
	}
}

func storedFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(root, "antigravity"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, d.Name())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func TestAntigravityNotInstalled(t *testing.T) {
	env, _ := newTestEnv(t, nil)
	env.RunCommand = func(cmd *exec.Cmd) error {
		t.Fatalf("unexpected command: %v", cmd.Args)
		return nil
	}
	g := &Antigravity{}

	st, err := g.Status(env)
	if err != nil || st.LoggedIn || !strings.Contains(st.Message, "tori g install") {
		t.Fatalf("unexpected status: %+v err=%v", st, err)
	}
	for name, run := range map[string]func() error{
		"login":  func() error { _, err := g.Login(env, "work"); return err },
		"logout": func() error { _, err := g.Logout(env); return err },
		"add":    func() error { _, err := g.Add(env, []string{"work"}); return err },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "tori g install") {
			t.Fatalf("%s: expected install hint, got %v", name, err)
		}
	}
	if slots, _ := g.Slots(env); len(slots) != 0 {
		t.Fatalf("expected no accounts saved, got %+v", slots)
	}
}

func TestAntigravityInstallRunsOfficialInstaller(t *testing.T) {
	cases := []struct {
		goos string
		bin  []string
		want []string
	}{
		{"linux", []string{".local", "bin", "agy"}, []string{"bash", "-c", "set -o pipefail; curl -fsSL https://antigravity.google/cli/install.sh | bash"}},
		{"windows", []string{"AppData", "Local", "agy", "bin", "agy.exe"}, []string{"powershell", "-NoProfile", "-Command", "irm https://antigravity.google/cli/install.ps1 | iex"}},
	}
	for _, tc := range cases {
		env, _ := newTestEnv(t, nil)
		env.GOOS = tc.goos
		var ran [][]string
		env.RunCommand = func(cmd *exec.Cmd) error {
			ran = append(ran, cmd.Args)
			return store.WriteBytesAtomic(filepath.Join(append([]string{env.Home}, tc.bin...)...), []byte("stub"), 0o700, false)
		}
		g := &Antigravity{}
		if _, err := g.Install(env); err != nil {
			t.Fatalf("%s install: %v", tc.goos, err)
		}
		if len(ran) != 1 || !reflect.DeepEqual(ran[0], tc.want) {
			t.Fatalf("%s ran %q, want %q", tc.goos, ran, tc.want)
		}
		// A second install finds agy and runs nothing.
		out, err := g.Install(env)
		if err != nil || len(ran) != 1 || !strings.Contains(out[0], "already installed") {
			t.Fatalf("%s reinstall: out=%v ran=%d err=%v", tc.goos, out, len(ran), err)
		}
	}
}

func TestAntigravityLoginSwitchLogoutKeepNamesOnly(t *testing.T) {
	f := newAgyFixture(t, nil)
	g := &Antigravity{}
	// A login belonging to agy: Tori must never write or delete it.
	f.keyring.items[agyKeyringService+"\x00"+agyKeyringUser] = `{"token":{"access_token":"a","refresh_token":"r"}}`

	f.login(t, "work")
	f.login(t, "personal")
	if want := []string{f.bin, "acp", "--login"}; !reflect.DeepEqual(f.ran[0], want) {
		t.Fatalf("login ran %q, want %q", f.ran[0], want)
	}
	if cur := f.current(t); cur != 2 {
		t.Fatalf("current = %d, want 2", cur)
	}

	// Rotating without naming an account is refused.
	if _, err := Switch(f.env, g, ""); err == nil || !strings.Contains(err.Error(), "choose the account") {
		t.Fatalf("expected explicit-account error, got %v", err)
	}
	// Switching to the marked account does not start a sign-in.
	n := len(f.ran)
	out, err := Switch(f.env, g, "personal")
	if err != nil || len(f.ran) != n || !strings.Contains(strings.Join(out, "\n"), "tori g login personal") {
		t.Fatalf("switch to current: out=%v ran=%d err=%v", out, len(f.ran), err)
	}
	// A cancelled sign-in leaves the marked account unchanged.
	f.tick()
	f.runErr = errors.New("exit status 1")
	if _, err := Switch(f.env, g, "work"); err == nil || !strings.Contains(err.Error(), "didn't finish") {
		t.Fatalf("expected sign-in failure, got %v", err)
	}
	if _, err := g.Login(f.env, "third"); err == nil {
		t.Fatal("expected failed login")
	}
	if slots, _ := g.Slots(f.env); len(slots) != 2 || f.current(t) != 2 {
		t.Fatalf("failed sign-in changed accounts: slots=%d current=%d", len(slots), f.current(t))
	}
	f.runErr = nil
	f.tick()
	if _, err := Switch(f.env, g, "work"); err != nil || f.current(t) != 1 {
		t.Fatalf("switch: current=%d err=%v", f.current(t), err)
	}
	// Signing in again by name reuses the slot.
	f.login(t, "personal")
	if slots, _ := g.Slots(f.env); len(slots) != 2 || f.current(t) != 2 {
		t.Fatalf("relogin: slots=%d current=%d", len(slots), f.current(t))
	}

	// The marker follows its account when slots are renumbered.
	slots, _ := g.Slots(f.env)
	if _, err := Remove(f.env, g, slots[0]); err != nil {
		t.Fatal(err)
	}
	if cur := f.current(t); cur != 1 {
		t.Fatalf("current after renumbering = %d, want 1", cur)
	}

	if _, err := g.Logout(f.env); err != nil {
		t.Fatal(err)
	}
	if want := []string{f.bin, "-i", "/logout"}; !reflect.DeepEqual(f.ran[len(f.ran)-1], want) {
		t.Fatalf("logout ran %q, want %q", f.ran[len(f.ran)-1], want)
	}
	if cur := f.current(t); cur != 0 {
		t.Fatalf("expected no marked account after logout, got %d", cur)
	}

	if got, want := storedFiles(t, f.env.Root), []string{"account.json", "meta.json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tori stored %v, want only %v", got, want)
	}
	if f.keyring.gets != 0 || f.keyring.writes != 0 {
		t.Fatalf("account commands touched the keyring: gets=%d writes=%d", f.keyring.gets, f.keyring.writes)
	}
}

func TestAntigravitySignInExplainsHowToCancel(t *testing.T) {
	for goos, want := range map[string]string{"linux": "Press Ctrl+D to cancel.", "windows": "Press Ctrl+Z, then Enter, to cancel."} {
		f := newAgyFixture(t, nil)
		var out strings.Builder
		f.env.GOOS = goos
		f.env.Stdout = &out
		f.envMap["TORI_AGY_BIN"] = f.bin
		f.login(t, "work")
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%s: missing %q in %q", goos, want, out.String())
		}
	}
}

func TestAntigravityAddNamesWithoutSigningIn(t *testing.T) {
	f := newAgyFixture(t, nil)
	g := &Antigravity{}

	if _, err := g.Add(f.env, nil); err == nil || !strings.Contains(err.Error(), "tori g add <name>") {
		t.Fatalf("expected name requirement, got %v", err)
	}
	if _, err := g.Login(f.env, "--oops"); err == nil || len(f.ran) != 0 {
		t.Fatalf("expected an option to be rejected before sign-in, err=%v ran=%q", err, f.ran)
	}
	if _, err := g.Add(f.env, []string{"7"}); err == nil {
		t.Fatal("expected unknown slot number to be rejected")
	}
	f.tick()
	if _, err := g.Add(f.env, []string{"work"}); err != nil {
		t.Fatal(err)
	}
	if len(f.ran) != 0 || f.current(t) != 1 {
		t.Fatalf("add ran %q, current=%d", f.ran, f.current(t))
	}

	// An unnamed login leaves no account marked.
	f.login(t, "")
	if f.current(t) != 0 {
		t.Fatalf("expected no marked account, got %d", f.current(t))
	}
}

func TestAntigravityLegacyTokenCleanup(t *testing.T) {
	f := newAgyFixture(t, nil)
	g := &Antigravity{}
	dir1 := store.SlotDir(f.env.Root, g.Key(), 1)
	dir2 := store.SlotDir(f.env.Root, g.Key(), 2)
	for path, v := range map[string]any{
		filepath.Join(dir1, "auth.json"):      map[string]any{"refreshToken": "rt"},
		filepath.Join(dir1, "access.json"):    map[string]any{"accessToken": "at"},
		filepath.Join(dir1, "meta.json"):      store.Meta{Email: "a@example.com", Alias: "work"},
		filepath.Join(dir2, "acp_token.json"): map[string]any{"refresh_token": "rt"},
		filepath.Join(dir2, "meta.json"):      store.Meta{Alias: "old"},
	} {
		if err := store.WriteJSONAtomic(path, v, 0o600, false); err != nil {
			t.Fatal(err)
		}
	}

	if slots, _ := g.Slots(f.env); len(slots) != 2 || f.current(t) != 0 {
		t.Fatalf("old slots: %d, current=%d", len(slots), f.current(t))
	}
	if files := g.LegacyTokenFiles(f.env); len(files) != 3 {
		t.Fatalf("legacy files = %v", files)
	}
	n, err := g.DeleteLegacyTokens(f.env)
	if err != nil || n != 3 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	slots, _ := g.Slots(f.env)
	if len(slots) != 2 || slots[0].Meta.Email != "a@example.com" || slots[1].Meta.Alias != "old" {
		t.Fatalf("accounts lost by cleanup: %+v", slots)
	}
	if got, want := storedFiles(t, f.env.Root), []string{"account.json", "meta.json"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files after cleanup %v, want %v", got, want)
	}
}

func agyQuotaTransport(t *testing.T, email *string, calls *int) http.RoundTripper {
	var mu sync.Mutex
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		*calls++
		if got := req.Header.Get("Authorization"); got != "Bearer live-access" {
			t.Errorf("unexpected authorization %q", got)
		}
		switch req.URL.String() {
		case agyUserinfoURL:
			return jsonResponse(200, map[string]any{"email": *email}), nil
		case agyDailyEndpoint + "/v1internal:retrieveUserQuotaSummary":
			return jsonResponse(200, map[string]any{}), nil // empty: try the next endpoint
		case agyProdEndpoint + "/v1internal:retrieveUserQuotaSummary":
			return jsonResponse(500, map[string]any{"error": "boom"}), nil
		case agyDailyEndpoint + "/v1internal:fetchAvailableModels":
			return jsonResponse(200, map[string]any{
				"models": map[string]any{
					"gemini-2.5-pro": map[string]any{
						"modelProvider":   "MODEL_PROVIDER_GOOGLE",
						"quotaInfo":       map[string]any{"remainingFraction": 0.75},
						"weeklyQuotaInfo": map[string]any{"remainingFraction": 0.4},
					},
				},
			}), nil
		default:
			t.Errorf("unexpected URL: %s", req.URL.String())
			return nil, errors.New("unexpected")
		}
	})
}

func agyTokenJSON(access string, expiry time.Time) string {
	return `{"token":{"access_token":"` + access + `","refresh_token":"never-used","expiry":"` +
		expiry.Format(time.RFC3339Nano) + `"},"auth_method":"consumer"}`
}

func TestAntigravityQuotaOnlyForSignedInAccount(t *testing.T) {
	email := "work@example.com"
	calls := 0
	f := newAgyFixture(t, agyQuotaTransport(t, &email, &calls))
	g := &Antigravity{}
	key := agyKeyringService + "\x00" + agyKeyringUser
	f.keyring.items[key] = agyTokenJSON("live-access", f.clock.Add(2*time.Hour))
	f.login(t, "work")
	f.login(t, "personal")
	f.tick()
	if _, err := Switch(f.env, g, "work"); err != nil {
		t.Fatal(err)
	}

	// Meters are off by default: no keyring read and no network.
	if _, _, err := Entries(f.env, g, true, false); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || f.keyring.gets != 0 {
		t.Fatalf("meters off: calls=%d keyring gets=%d", calls, f.keyring.gets)
	}

	if err := config.SetUsage(f.env.Root, []string{"antigravity"}, true); err != nil {
		t.Fatal(err)
	}
	entries, _, err := Entries(f.env, g, true, false)
	if err != nil {
		t.Fatal(err)
	}
	u := entries[0].Usage
	if u == nil || len(u.Windows) != 2 || u.Windows[0].Pct != 25 || u.Windows[1].Pct != 60 {
		t.Fatalf("signed-in account usage: %+v", u)
	}
	if entries[0].Email != "work@example.com" {
		t.Fatalf("expected learned email, got %q", entries[0].Email)
	}
	if entries[1].Usage != nil {
		t.Fatalf("other account must not be read: %+v", entries[1].Usage)
	}
	if f.keyring.writes != 0 {
		t.Fatalf("quota wrote to the keyring %d times", f.keyring.writes)
	}

	// A different account signed in directly in agy must not be shown as this one.
	email = "someone-else@example.com"
	f.clock = f.clock.Add(10 * time.Minute)
	entries, _, err = Entries(f.env, g, true, false)
	if err != nil {
		t.Fatal(err)
	}
	u = entries[0].Usage
	if u == nil || u.Error == nil || *u.Error != "another account is signed in" || entries[0].Email != "work@example.com" {
		t.Fatalf("mismatched account: %+v email=%q", u, entries[0].Email)
	}
}

func TestAntigravityQuotaNeverRenewsExpiredLogin(t *testing.T) {
	calls := 0
	email := "work@example.com"
	f := newAgyFixture(t, agyQuotaTransport(t, &email, &calls))
	g := &Antigravity{}
	// No keyring entry: agy's token file is the fallback.
	tokenFile := filepath.Join(f.env.Home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := store.WriteBytesAtomic(tokenFile, []byte(agyTokenJSON("live-access", f.clock.Add(-time.Hour))), 0o600, false); err != nil {
		t.Fatal(err)
	}
	f.login(t, "work")
	if err := config.SetUsage(f.env.Root, []string{"antigravity"}, true); err != nil {
		t.Fatal(err)
	}
	entries, _, err := Entries(f.env, g, true, false)
	if err != nil {
		t.Fatal(err)
	}
	u := entries[0].Usage
	if u == nil || u.Error == nil || *u.Error != g.PermError() {
		t.Fatalf("expected renew hint, got %+v", u)
	}
	if calls != 0 {
		t.Fatalf("expired login must not reach the network, calls=%d", calls)
	}

	if err := os.WriteFile(tokenFile, []byte(agyTokenJSON("live-access", f.clock.Add(time.Hour))), 0o600); err != nil {
		t.Fatal(err)
	}
	f.clock = f.clock.Add(10 * time.Minute)
	entries, _, err = Entries(f.env, g, true, false)
	if err != nil || entries[0].Usage == nil || len(entries[0].Usage.Windows) != 2 {
		t.Fatalf("token file fallback: %+v err=%v", entries[0].Usage, err)
	}
}
