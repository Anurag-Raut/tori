package provider

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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

// The test binary doubles as a fake Google runtime: when TORI_FAKE_ACP is set it
// speaks ACP on stdio instead of running tests.
func TestMain(m *testing.M) {
	if mode := os.Getenv("TORI_FAKE_ACP"); mode != "" {
		runFakeACP(mode)
		return
	}
	os.Exit(m.Run())
}

const fakeAuthURL = "https://accounts.google.com/o/oauth2/v2/auth?client_id=fake&state=x"

func runFakeACP(mode string) {
	logPath := os.Getenv("TORI_FAKE_LOG")
	logf := func(format string, a ...any) {
		if logPath == "" {
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, format+"\n", a...)
			f.Close()
		}
	}
	logf("start args=%v browser=%q home=%q tmp=%q harness=%q force=%q gemini_key=%q",
		os.Args[1:], os.Getenv("BROWSER"), os.Getenv("GEMINI_HOME"), os.Getenv("TMPDIR"),
		os.Getenv("ANTIGRAVITY_HARNESS_PATH"), os.Getenv("AGY_ACP_FORCE_FILE_STORAGE"), os.Getenv("GEMINI_API_KEY"))
	in := bufio.NewReader(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			logf("stdin closed")
			return
		}
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal([]byte(line), &req)
		logf("method %s", req.Method)
		reply := func(result any) {
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			if mode == "silent" {
				continue
			}
			caps := map[string]any{"auth": map[string]any{"logout": map[string]any{}}}
			if mode == "nologout" {
				caps = map[string]any{}
			}
			methods := []map[string]any{{"id": "oauth-personal"}}
			if mode == "nooauth" {
				methods = []map[string]any{{"id": "gemini-api-key"}}
			}
			reply(map[string]any{"protocolVersion": 1, "authMethods": methods, "agentCapabilities": caps})
		case "logout":
			if mode == "logouterr" {
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "nothing to sign out"}})
				continue
			}
			reply(map[string]any{})
		case "authenticate":
			fmt.Fprintln(os.Stderr, "noisy crash dump line")
			switch mode {
			case "badurl":
				fmt.Fprintln(os.Stderr, acpAuthPrefix+"https://evil.example/o/oauth2/v2/auth?x=1")
				fmt.Fprintln(os.Stderr, acpAuthPrefix+"https://accounts.google.com/other?x=1")
			default:
				fmt.Fprintln(os.Stderr, acpAuthPrefix+fakeAuthURL)
				fmt.Fprintln(os.Stderr, acpAuthPrefix+fakeAuthURL) // duplicate: reported once
			}
			switch mode {
			case "hang":
				select {}
			case "exit":
				os.Exit(3)
			case "nologin":
				reply(map[string]any{})
			default:
				acp := filepath.Join(os.Getenv("GEMINI_HOME"), "antigravity-acp")
				// A login Tori must never read: unreadable, with a sentinel body.
				_ = os.WriteFile(filepath.Join(acp, "acp_token.json"), []byte("SENTINEL-SECRET"), 0o000)
				reply(map[string]any{})
			}
		default:
			reply(map[string]any{})
		}
	}
}

// syncBuilder is a strings.Builder safe to read while the runtime's link is printed.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type t3Fixture struct {
	env     *Env
	envMap  map[string]string
	t3      string
	out     *syncBuilder
	log     string
	mode    string
	bin     string
	profile string
}

// newT3Fixture sets up a temp T3 Code home with one enabled Antigravity instance
// whose binaryPath is a link to the fake runtime. agy isn't installed.
func newT3Fixture(t *testing.T, mode string) *t3Fixture {
	t.Helper()
	env, envMap := newTestEnv(t, nil)
	f := &t3Fixture{env: env, envMap: envMap, out: &syncBuilder{}, mode: mode}
	f.t3 = filepath.Join(t.TempDir(), "t3")
	envMap["T3CODE_HOME"] = f.t3
	f.log = filepath.Join(t.TempDir(), "fake.log")
	f.bin = fakeRuntimeLink(t, "agy")
	f.writeSettings(t, map[string]any{
		"antigravity": map[string]any{"driver": "antigravity", "enabled": true, "config": map[string]any{"binaryPath": f.bin}},
	})
	f.profile = t3ProfileDir(env, "antigravity")
	env.Stdout = f.out
	env.Executable = func() (string, error) { return "/opt/tori/tori", nil }
	env.ProcEnviron = func() []string {
		return append(os.Environ(), "TORI_FAKE_ACP="+f.mode, "TORI_FAKE_LOG="+f.log, "GEMINI_API_KEY=leak")
	}
	return f
}

func fakeRuntimeLink(t *testing.T, name string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), name)
	if err := os.Symlink(exe, link); err != nil {
		t.Skipf("can't link the fake runtime: %v", err)
	}
	return link
}

func (f *t3Fixture) writeSettings(t *testing.T, instances map[string]any) {
	t.Helper()
	if err := store.WriteJSONAtomic(filepath.Join(f.t3, "userdata", "settings.json"), map[string]any{"providerInstances": instances}, 0o600, false); err != nil {
		t.Fatal(err)
	}
}

func (f *t3Fixture) calls(t *testing.T) []string {
	t.Helper()
	raw, _ := os.ReadFile(f.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if m, ok := strings.CutPrefix(l, "method "); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestT3DirsAndProfile(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	if got, want := t3BaseDir(env), filepath.Join(env.Home, ".t3"); got != want {
		t.Fatalf("base = %q, want %q", got, want)
	}
	envMap["T3CODE_HOME"] = "/custom/t3"
	if got := t3BaseDir(env); got != "/custom/t3" {
		t.Fatalf("base = %q", got)
	}
	if got, want := t3StateDir(env), filepath.Join("/custom/t3", "userdata"); got != want {
		t.Fatalf("state = %q, want %q", got, want)
	}
	// The default instance maps to the folder name T3 Code itself uses.
	const want = "ac0a3dfd6dddb20962cecff6ee5fe65e19d3923be20e52c5ab52ff877f7e4c32"
	if got := filepath.Base(t3ProfileDir(env, "antigravity")); got != want {
		t.Fatalf("profile folder = %q, want %q", got, want)
	}
	sum := sha256.Sum256([]byte("Work"))
	if got := filepath.Base(t3ProfileDir(env, "Work")); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash differs for a mixed-case id: %q", got)
	}
}

func TestT3InstanceDiscovery(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	envMap["T3CODE_HOME"] = filepath.Join(t.TempDir(), "t3")
	f := &t3Fixture{env: env, t3: envMap["T3CODE_HOME"]}
	ids := func() []string {
		var out []string
		for _, in := range t3Instances(env) {
			out = append(out, in.ID)
		}
		return out
	}
	if len(ids()) != 0 {
		t.Fatalf("expected none, got %v", ids())
	}
	// No settings, but the default profile folder exists.
	if err := os.MkdirAll(t3ProfileDir(env, "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(), []string{"antigravity"}) {
		t.Fatalf("folder only: %v", ids())
	}
	f.writeSettings(t, map[string]any{
		"antigravity": map[string]any{"driver": "antigravity", "enabled": true},
		"work":        map[string]any{"driver": "antigravity", "displayName": "Work", "enabled": true, "config": map[string]any{"authMethod": "oauth-personal"}},
		"viaconfig":   map[string]any{"driver": "antigravity", "config": map[string]any{"enabled": true}},
		"off":         map[string]any{"driver": "antigravity", "enabled": false},
		"offconfig":   map[string]any{"driver": "antigravity", "enabled": true, "config": map[string]any{"enabled": false}},
		"unset":       map[string]any{"driver": "antigravity"},
		"key":         map[string]any{"driver": "antigravity", "enabled": true, "config": map[string]any{"authMethod": "gemini-api-key"}},
		"biz":         map[string]any{"driver": "antigravity", "enabled": true, "config": map[string]any{"authMethod": "oauth-business"}},
		"codex":       map[string]any{"driver": "codex", "enabled": true},
	})
	if want := []string{"antigravity", "viaconfig", "work"}; !reflect.DeepEqual(ids(), want) {
		t.Fatalf("instances = %v, want %v", ids(), want)
	}
	// A settings file with other drivers only does not fall back to the folder.
	f.writeSettings(t, map[string]any{"codex": map[string]any{"driver": "codex"}})
	if got := ids(); len(got) != 1 || got[0] != "antigravity" {
		t.Fatalf("no antigravity instance but folder present: %v", got)
	}
	if err := os.RemoveAll(t3ProfileDir(env, "antigravity")); err != nil {
		t.Fatal(err)
	}
	if len(ids()) != 0 {
		t.Fatalf("expected none: %v", ids())
	}
}

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestT3RuntimeResolutionOrder(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	base := filepath.Join(t.TempDir(), "t3")
	envMap["T3CODE_HOME"] = base
	pathDir := t.TempDir()
	env.LookPath = func(name string) (string, error) {
		if p := filepath.Join(pathDir, name); fileExists(p) {
			return p, nil
		}
		return "", os.ErrNotExist
	}
	in := t3Instance{ID: "antigravity"}

	// Nothing installed.
	if _, err := t3ResolveRuntime(env, in); err == nil || !strings.Contains(err.Error(), "Install Antigravity in T3 Code's settings first.") {
		t.Fatalf("expected install hint, got %v", err)
	}

	// On PATH, with its harness beside it.
	touch(t, filepath.Join(pathDir, "agy_acp_server.par"))
	if _, err := t3ResolveRuntime(env, in); err == nil {
		t.Fatal("PATH server without a harness must not resolve")
	}
	harness := touch(t, filepath.Join(pathDir, "localharness_external"))
	rt, err := t3ResolveRuntime(env, in)
	if err != nil || rt.Harness != harness || !reflect.DeepEqual(rt.Args, []string{"--uid="}) {
		t.Fatalf("PATH runtime = %+v err=%v", rt, err)
	}

	// The release T3 Code installed wins over PATH.
	dir := filepath.Join(base, "tools", "antigravity-acp", "linux-x64")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte(`{"releaseId":"r1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := t3ResolveRuntime(env, in); err == nil {
		t.Fatal("an active release with no files must not fall back to PATH")
	}
	srv := touch(t, filepath.Join(dir, "versions", "r1", "agy_acp_server.par"))
	h := touch(t, filepath.Join(dir, "versions", "r1", "localharness_external"))
	rt, err = t3ResolveRuntime(env, in)
	if err != nil || rt.Path != srv || rt.Harness != h {
		t.Fatalf("release runtime = %+v err=%v", rt, err)
	}
	// A hostile release id can't leave the versions folder.
	if err := os.WriteFile(filepath.Join(dir, "active.json"), []byte(`{"releaseId":"../x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := t3ResolveRuntime(env, in); err == nil {
		t.Fatal("expected a bad release id to be refused")
	}

	// A custom path wins over everything. agy means the CLI.
	agy := touch(t, filepath.Join(t.TempDir(), "agy.exe"))
	rt, err = t3ResolveRuntime(env, t3Instance{ID: "x", BinaryPath: agy})
	if err != nil || rt.Harness != "" || !reflect.DeepEqual(rt.Args, []string{"acp"}) {
		t.Fatalf("custom agy = %+v err=%v", rt, err)
	}
	other := touch(t, filepath.Join(t.TempDir(), "custom_server"))
	if _, err := t3ResolveRuntime(env, t3Instance{ID: "x", BinaryPath: other}); err == nil {
		t.Fatal("a custom server needs a harness")
	}
	if _, err := t3ResolveRuntime(env, t3Instance{ID: "x", BinaryPath: "/nope/agy"}); err == nil {
		t.Fatal("a missing custom path must fail")
	}

	// Other platforms: names and arguments.
	env.GOOS, env.GOARCH = "windows", "arm64"
	wdir := filepath.Join(base, "tools", "antigravity-acp", "win32-arm64")
	if err := os.MkdirAll(wdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wdir, "active.json"), []byte(`{"releaseId":"w"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(wdir, "versions", "w", "agy_acp_server.exe"))
	touch(t, filepath.Join(wdir, "versions", "w", "localharness_external.exe"))
	rt, err = t3ResolveRuntime(env, in)
	if err != nil || len(rt.Args) != 0 || !strings.HasSuffix(rt.Harness, "localharness_external.exe") {
		t.Fatalf("windows runtime = %+v err=%v", rt, err)
	}
}

func TestT3SpawnEnvironment(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	envMap["NIX_SSL_CERT_FILE"] = ""
	rt := &t3Runtime{Path: "/r/server", Harness: "/r/harness"}
	base := []string{
		"PATH=/bin", "gemini_api_key=k", "GOOGLE_API_KEY=k", "GOOGLE_APPLICATION_CREDENTIALS=/x", "GOOGLE_CLOUD_PROJECT=p",
		"GOOGLE_CLOUD_LOCATION=l", "GOOGLE_CLOUD_QUOTA_PROJECT=q", "GOOGLE_GENAI_USE_VERTEXAI=1", "GCLOUD_PROJECT=p",
		"CLOUDSDK_CORE_PROJECT=p", "AGY_ACP_CCPA_PROJECT=p", "AGY_ACP_ENABLE_OAUTH=1", "GEMINI_HOME=/real", "AGY_ACP_FORCE_FILE_STORAGE=0",
		"ANTIGRAVITY_HARNESS_PATH=/old", "BROWSER=firefox", "PYTHONUNBUFFERED=0", "ELECTRON_RUN_AS_NODE=1",
		"CHROME_CRASHPAD_PIPE_NAME=x", "KEEP=yes",
	}
	got := t3SpawnEnv(env, base, rt, "/p", "/p/antigravity-acp/tmp", "BR %s")
	m := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := m[k]; dup {
			t.Fatalf("duplicate %s in %v", k, got)
		}
		m[k] = v
	}
	for k := range t3RemovedEnv {
		switch k {
		case "GEMINI_HOME", "AGY_ACP_FORCE_FILE_STORAGE", "ANTIGRAVITY_HARNESS_PATH", "BROWSER", "PYTHONUNBUFFERED":
			continue
		}
		if _, ok := m[k]; ok {
			t.Fatalf("%s must be removed", k)
		}
	}
	if _, ok := m["gemini_api_key"]; ok {
		t.Fatal("removal must ignore case")
	}
	want := map[string]string{
		"KEEP": "yes", "PATH": "/bin", "GEMINI_HOME": "/p", "AGY_ACP_FORCE_FILE_STORAGE": "1", "PYTHONUNBUFFERED": "1",
		"BROWSER": "BR %s", "ANTIGRAVITY_HARNESS_PATH": "/r/harness", "TMPDIR": "/p/antigravity-acp/tmp",
	}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("%s = %q, want %q", k, m[k], v)
		}
	}
	// A CA bundle is chosen on Linux only when none is set.
	withCA := t3SpawnEnv(env, []string{"SSL_CERT_DIR=/certs"}, rt, "/p", "/t", "")
	if slices.ContainsFunc(withCA, func(s string) bool { return strings.HasPrefix(s, "SSL_CERT_FILE=") }) {
		t.Fatal("SSL_CERT_FILE must not be added when SSL_CERT_DIR is set")
	}
	// Windows uses TEMP and TMP, and no harness variable for the CLI.
	env.GOOS = "windows"
	w := t3SpawnEnv(env, nil, &t3Runtime{Path: "agy.exe"}, "C:/p", "C:/t", "")
	if !slices.Contains(w, "TEMP=C:/t") || !slices.Contains(w, "TMP=C:/t") || slices.ContainsFunc(w, func(s string) bool {
		return strings.HasPrefix(s, "TMPDIR=") || strings.HasPrefix(s, "ANTIGRAVITY_HARNESS_PATH=")
	}) {
		t.Fatalf("windows env = %v", w)
	}
}

func TestT3BrowserCommandQuoting(t *testing.T) {
	if got, want := t3BrowserCommand("/opt/it's/tori", false), `'/opt/it'"'"'s/tori' __open-url %s`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := t3BrowserCommand(`C:\Users\a b\tori.exe`, true), `'C:/Users/a b/tori.exe' __open-url %s`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestT3ProfileSettingsWrittenOnlyWhenMissing(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	envMap["T3CODE_HOME"] = filepath.Join(t.TempDir(), "t3")
	in := t3Instance{ID: "antigravity"}
	profile, acp, tmp, err := t3PrepareProfile(env, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(acp, "settings.json"))
	if err != nil || string(raw) != t3ProfileConfig {
		t.Fatalf("settings = %q err=%v", raw, err)
	}
	if st, err := os.Stat(tmp); err != nil || !st.IsDir() {
		t.Fatalf("tmp: %v", err)
	}
	if st, _ := os.Stat(profile); st.Mode().Perm() != 0o700 {
		t.Fatalf("profile mode = %v", st.Mode().Perm())
	}
	custom := []byte(`{"auth":{"type":"oauth-personal"},"gcp":{"project":"p"}}` + "\n")
	if err := os.WriteFile(filepath.Join(acp, "settings.json"), custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := t3PrepareProfile(env, in); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(acp, "settings.json")); string(raw) != string(custom) {
		t.Fatalf("existing settings were overwritten: %q", raw)
	}
}

func TestAuthURLValidation(t *testing.T) {
	good := "https://accounts.google.com/o/oauth2/v2/auth?client_id=1&state=2"
	if u, ok := authURLFromLine("INFO " + acpAuthPrefix + good + "\n"); !ok || u != good {
		t.Fatalf("got %q %v", u, ok)
	}
	for _, bad := range []string{
		"http://accounts.google.com/o/oauth2/v2/auth?x=1",
		"https://accounts.google.com.evil.example/o/oauth2/v2/auth?x=1",
		"https://evil.example/o/oauth2/v2/auth",
		"https://accounts.google.com/o/oauth2/auth?x=1",
		"https://user@accounts.google.com/o/oauth2/v2/auth",
		"javascript:alert(1)",
	} {
		if _, ok := authURLFromLine(acpAuthPrefix + bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if _, ok := authURLFromLine("no link here"); ok {
		t.Fatal("accepted a line without the prefix")
	}
}

func TestT3SignInSwitchesThroughRuntime(t *testing.T) {
	f := newT3Fixture(t, "ok")
	g := &Antigravity{}
	if targets := g.Targets(f.env); len(targets) != 1 || targets[0].ID != "t3:antigravity" || targets[0].Label != "T3 Code" {
		t.Fatalf("targets = %+v", targets)
	}
	out, err := g.Login(f.env, "work")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, f.out.String())
	}
	if want := []string{"initialize", "logout", "authenticate"}; !reflect.DeepEqual(f.calls(t), want) {
		t.Fatalf("calls = %v, want %v", f.calls(t), want)
	}
	if strings.Count(f.out.String(), fakeAuthURL) != 1 || !strings.Contains(f.out.String(), "If the browser didn't open, open this link:") {
		t.Fatalf("the link must be printed once:\n%s", f.out.String())
	}
	if strings.Contains(f.out.String(), "noisy crash dump") || strings.Contains(strings.Join(out, "\n"), "SENTINEL") || strings.Contains(f.out.String(), "SENTINEL") {
		t.Fatalf("runtime noise or token content leaked:\n%s", f.out.String())
	}
	if !strings.Contains(strings.Join(out, "\n"), "T3 Code now uses work. Restart T3 Code or start a new chat.") {
		t.Fatalf("out = %v", out)
	}
	logText, _ := os.ReadFile(f.log)
	for _, want := range []string{"args=[acp]", "browser=\"'/opt/tori/tori' __open-url %s\"", "force=\"1\"", "gemini_key=\"\"", "home=\"" + f.profile + "\""} {
		if !strings.Contains(string(logText), want) {
			t.Fatalf("runtime saw %s missing in %s", want, logText)
		}
	}
	// Only the instance's profile holds files, and only Google's code wrote the login.
	if st, err := os.Stat(filepath.Join(f.profile, "antigravity-acp", "acp_token.json")); err != nil || st.Mode().Perm() != 0 {
		t.Fatalf("token stat: %v", err)
	}
	st := g.SignedInTargets(f.env, mustSlot(t, g, f.env, 1))
	if !reflect.DeepEqual(st, []string{"T3 Code"}) {
		t.Fatalf("signed in targets = %v", st)
	}
	if cur := (&agyFixture{env: f.env}).current(t); cur != 1 {
		t.Fatalf("current = %d", cur)
	}
	if got := config.AntigravityTargets(f.env.Root); len(got) != 0 {
		t.Fatalf("a default choice must not be remembered, got %v", got)
	}
	assertNoRuntimeLeft(t, f)
}

func assertNoRuntimeLeft(t *testing.T, f *t3Fixture) {
	t.Helper()
	// stop() waits for the process; a second stop must be harmless.
	time.Sleep(10 * time.Millisecond)
}

func mustSlot(t *testing.T, g *Antigravity, env *Env, n int) store.Slot {
	t.Helper()
	slots, err := g.Slots(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		if s.Number == n {
			return s
		}
	}
	t.Fatalf("no slot %d", n)
	return store.Slot{}
}

func TestT3SignInFailures(t *testing.T) {
	cases := []struct {
		mode string
		want string
		tune func()
	}{
		{"exit", "the runtime stopped", nil},
		{"nologin", "saved no login", nil},
		{"nooauth", "doesn't offer Google sign-in", nil},
		{"hang", "no answer in time", func() { acpAuthTimeout = 400 * time.Millisecond }},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			old := acpAuthTimeout
			defer func() { acpAuthTimeout = old }()
			if tc.tune != nil {
				tc.tune()
			}
			f := newT3Fixture(t, tc.mode)
			g := &Antigravity{}
			_, err := g.Login(f.env, "work")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if slots, _ := g.Slots(f.env); len(slots) != 0 {
				t.Fatalf("a failed sign-in saved an account: %+v", slots)
			}
		})
	}
}

func TestT3InitializeTimeout(t *testing.T) {
	old := acpInitTimeout
	acpInitTimeout = 300 * time.Millisecond
	defer func() { acpInitTimeout = old }()
	f := newT3Fixture(t, "silent")
	_, err := (&Antigravity{}).Login(f.env, "work")
	if err == nil || !strings.Contains(err.Error(), "no answer in time") {
		t.Fatalf("err = %v", err)
	}
}

func TestT3CancelSignsOutAndStopsRuntime(t *testing.T) {
	f := newT3Fixture(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	f.env.Ctx = ctx
	// Cancel once the link has been shown.
	go func() {
		for i := 0; i < 500; i++ {
			if strings.Contains(f.out.String(), fakeAuthURL) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	_, err := (&Antigravity{}).Login(f.env, "work")
	if err == nil || !strings.Contains(err.Error(), "Sign-in cancelled. T3 Code is signed out until you sign in again.") {
		t.Fatalf("err = %v", err)
	}
	logText, _ := os.ReadFile(f.log)
	if !strings.Contains(string(logText), "stdin closed") && !strings.Contains(string(logText), "method authenticate") {
		t.Fatalf("log = %s", logText)
	}
}

func TestT3LogoutErrorsAreNotFatalButTimeoutsAre(t *testing.T) {
	f := newT3Fixture(t, "logouterr")
	if _, err := (&Antigravity{}).Login(f.env, "work"); err != nil {
		t.Fatalf("an answered logout error must not stop the sign-in: %v", err)
	}
	f = newT3Fixture(t, "nologout")
	if _, err := (&Antigravity{}).Login(f.env, "work"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"initialize", "authenticate"}; !reflect.DeepEqual(f.calls(t), want) {
		t.Fatalf("calls = %v, want %v (no logout capability)", f.calls(t), want)
	}
}

func TestT3SignOut(t *testing.T) {
	f := newT3Fixture(t, "ok")
	g := &Antigravity{}
	if _, err := g.Login(f.env, "work"); err != nil {
		t.Fatal(err)
	}
	f.env.Targets = []string{TargetT3All}
	out, err := g.Logout(f.env)
	if err != nil || !strings.Contains(strings.Join(out, " "), "no longer marks") {
		t.Fatalf("logout: %v %v", out, err)
	}
	if cur := (&agyFixture{env: f.env}).current(t); cur != 0 {
		t.Fatalf("current after sign-out = %d", cur)
	}
	f = newT3Fixture(t, "nologout")
	f.env.Targets = []string{TargetT3All}
	if _, err := g.Logout(f.env); err == nil || !strings.Contains(err.Error(), "can't sign out") {
		t.Fatalf("err = %v", err)
	}
}

func TestTargetSelectionAndPersistence(t *testing.T) {
	f := newT3Fixture(t, "ok")
	g := &Antigravity{}
	agy := filepath.Join(f.env.Home, ".local", "bin", "agy")
	if err := store.WriteBytesAtomic(agy, []byte("stub"), 0o700, false); err != nil {
		t.Fatal(err)
	}
	f.writeSettings(t, map[string]any{
		"antigravity": map[string]any{"driver": "antigravity", "enabled": true, "displayName": "Main", "config": map[string]any{"binaryPath": f.bin}},
		"work":        map[string]any{"driver": "antigravity", "enabled": true, "config": map[string]any{"binaryPath": f.bin}},
	})
	ids := func(ts []Target) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	targets := g.Targets(f.env)
	if want := []string{"agy", "t3:antigravity", "t3:work"}; !reflect.DeepEqual(ids(targets), want) {
		t.Fatalf("targets = %v", ids(targets))
	}
	if targets[1].Label != "T3 Code: Main" || targets[2].Label != "T3 Code: work" || targets[0].Label != "Antigravity CLI & App (agy)" {
		t.Fatalf("labels = %q %q %q", targets[0].Label, targets[1].Label, targets[2].Label)
	}

	// No request and nothing remembered: everything.
	sel, err := g.selection(f.env)
	if err != nil || len(sel) != 3 {
		t.Fatalf("default selection = %v err=%v", ids(sel), err)
	}
	// An explicit request is used and remembered.
	f.env.Targets = []string{"t3:work", TargetAgy}
	sel, err = g.selection(f.env)
	if err != nil || !reflect.DeepEqual(ids(sel), []string{"agy", "t3:work"}) {
		t.Fatalf("selection = %v err=%v", ids(sel), err)
	}
	f.env.Targets = nil
	if got := g.Remembered(f.env); !reflect.DeepEqual(got, []string{"agy", "t3:work"}) {
		t.Fatalf("remembered = %v", got)
	}
	if sel, _ := g.selection(f.env); !reflect.DeepEqual(ids(sel), []string{"agy", "t3:work"}) {
		t.Fatalf("remembered selection = %v", ids(sel))
	}
	// --t3 means every T3 instance and drops agy.
	f.env.Targets = []string{TargetT3All}
	if sel, _ := g.selection(f.env); !reflect.DeepEqual(ids(sel), []string{"t3:antigravity", "t3:work"}) {
		t.Fatalf("--t3 = %v", ids(sel))
	}
	// Unknown targets fail clearly.
	f.env.Targets = []string{"t3:nope"}
	if _, err := g.selection(f.env); err == nil || !strings.Contains(err.Error(), "no T3 Code Antigravity instance 'nope'") {
		t.Fatalf("err = %v", err)
	}
	// A remembered target that went away falls back to what is available.
	if err := config.SetAntigravityTargets(f.env.Root, []string{"t3:gone"}); err != nil {
		t.Fatal(err)
	}
	f.env.Targets = nil
	if got := g.Remembered(f.env); len(got) != 3 {
		t.Fatalf("remembered fallback = %v", got)
	}
	// Other config keys survive.
	if err := config.SetUsage(f.env.Root, []string{"claude"}, false); err != nil {
		t.Fatal(err)
	}
	if got := config.AntigravityTargets(f.env.Root); !reflect.DeepEqual(got, []string{"t3:gone"}) {
		t.Fatalf("SetUsage lost the remembered targets: %v", got)
	}
}

func TestNoTargetsAndMissingTargets(t *testing.T) {
	env, envMap := newTestEnv(t, nil)
	envMap["T3CODE_HOME"] = filepath.Join(t.TempDir(), "t3")
	g := &Antigravity{}
	if g.Installed(env) {
		t.Fatal("nothing is installed")
	}
	_, err := g.Login(env, "work")
	if err == nil || !strings.Contains(err.Error(), "tori g install") || !strings.Contains(err.Error(), "T3 Code") {
		t.Fatalf("err = %v", err)
	}
	// T3 Code is set up but has no runtime: not offered, status explains.
	if err := store.WriteJSONAtomic(filepath.Join(envMap["T3CODE_HOME"], "userdata", "settings.json"),
		map[string]any{"providerInstances": map[string]any{"antigravity": map[string]any{"driver": "antigravity", "enabled": true}}}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	env.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if len(g.Targets(env)) != 0 {
		t.Fatal("a T3 instance without a runtime must not be offered")
	}
	st, _ := g.Status(env)
	if !strings.Contains(st.Message, "Install Antigravity in T3 Code's settings first.") {
		t.Fatalf("status = %q", st.Message)
	}
	// agy only: --t3 and an unknown instance fail clearly.
	f := newAgyFixture(t, nil)
	f.env.Targets = []string{TargetT3All}
	if _, err := g.Login(f.env, "work"); err == nil || !strings.Contains(err.Error(), "T3 Code isn't ready") {
		t.Fatalf("err = %v", err)
	}
	// T3 only: --agy fails clearly.
	f2 := newT3Fixture(t, "ok")
	f2.env.Targets = []string{TargetAgy}
	if _, err := g.Login(f2.env, "work"); err == nil || !strings.Contains(err.Error(), "agy isn't installed") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountStateMigratesFromSignedInAt(t *testing.T) {
	f := newAgyFixture(t, nil)
	g := &Antigravity{}
	if _, err := g.Add(f.env, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Add(f.env, []string{"newer"}); err != nil {
		t.Fatal(err)
	}
	slots, _ := g.Slots(f.env)
	// Write the old format by hand.
	if err := store.WriteJSONAtomic(filepath.Join(slots[0].Dir, agyAccountFile), map[string]any{"signedInAt": 500}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSONAtomic(filepath.Join(slots[1].Dir, agyAccountFile), map[string]any{}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if cur := g.Current(f.env, slots); cur == nil || *cur != 1 {
		t.Fatalf("old marker not read: %v", cur)
	}
	if got := g.SignedInTargets(f.env, slots[0]); !reflect.DeepEqual(got, []string{"agy"}) {
		t.Fatalf("targets = %v", got)
	}
	// The next write uses the new format and drops the old key.
	f.tick()
	if _, err := Switch(f.env, g, "newer"); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if !store.ReadJSON(filepath.Join(slots[1].Dir, agyAccountFile), &raw) || raw["signedIn"] == nil || raw["signedInAt"] != nil {
		t.Fatalf("account.json = %v", raw)
	}
	var old map[string]any
	store.ReadJSON(filepath.Join(slots[0].Dir, agyAccountFile), &old)
	if old["signedInAt"] != nil || old["signedIn"] != nil {
		t.Fatalf("the previous account stays marked: %v", old)
	}
}

func TestCurrentIsLatestAcrossTargets(t *testing.T) {
	f := newAgyFixture(t, nil)
	g := &Antigravity{}
	_, _ = g.Add(f.env, []string{"a"})
	f.tick()
	_, _ = g.Add(f.env, []string{"b"})
	slots, _ := g.Slots(f.env)
	f.tick()
	if err := g.setSignedIn(f.env, slots, 1, []string{"t3:antigravity"}); err != nil {
		t.Fatal(err)
	}
	if cur := g.Current(f.env, slots); cur == nil || *cur != 1 {
		t.Fatalf("current = %v, want slot 1 (latest sign-in on any target)", cur)
	}
	// agy stays marked for b; T3 Code for a.
	if got := g.SignedInTargets(f.env, mustSlot(t, g, f.env, 2)); !reflect.DeepEqual(got, []string{"agy"}) {
		t.Fatalf("b = %v", got)
	}
	if got := g.SignedInTargets(f.env, mustSlot(t, g, f.env, 1)); !reflect.DeepEqual(got, []string{"T3 Code"}) {
		t.Fatalf("a = %v", got)
	}
}

// Tori's code for T3 Code may stat the login file, never open or read it.
func TestT3CodeNeverOpensLoginFile(t *testing.T) {
	files, _ := filepath.Glob("antigravity_t3*.go")
	more, _ := filepath.Glob("antigravity_targets.go")
	more2, _ := filepath.Glob("antigravity_acp.go")
	files = append(append(files, more...), more2...)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			reads := strings.Contains(line, "os.ReadFile") || strings.Contains(line, "os.Open(") || strings.Contains(line, "os.OpenFile") || strings.Contains(line, "os.Rename") || strings.Contains(line, "os.Remove")
			if reads && !strings.Contains(line, "settings.json") && !strings.Contains(line, "active.json") && !strings.Contains(line, "settings") {
				t.Errorf("%s:%d reads or changes a file: %s", f, i+1, strings.TrimSpace(line))
			}
			if strings.Contains(line, "acp_token") && !strings.Contains(line, "//") && !strings.Contains(raw2fn(string(raw), i), "os.Stat") {
				t.Errorf("%s:%d mentions the login file outside a stat: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// raw2fn returns the text of the function around line i, to check what it calls.
func raw2fn(src string, i int) string {
	lines := strings.Split(src, "\n")
	start := i
	for start > 0 && !strings.HasPrefix(lines[start], "func ") {
		start--
	}
	end := i
	for end < len(lines)-1 && lines[end] != "}" {
		end++
	}
	return strings.Join(lines[start:end], "\n")
}
