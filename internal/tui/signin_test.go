package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Anurag-Raut/tori/internal/provider"
	"github.com/Anurag-Raut/tori/internal/store"
)

// signInModel returns a model with agy installed whose sign-ins run in-process.
func signInModel(t *testing.T, accounts ...string) (Model, *provider.Env, *[][]string, *error) {
	t.Helper()
	withASCIIProfile(t)
	env, envMap := setupTestEnv(t, nil)
	env.DisableNetwork = true
	bin := env.Home + "/agy"
	if err := store.WriteBytesAtomic(bin, []byte("stub"), 0o700, false); err != nil {
		t.Fatal(err)
	}
	envMap["TORI_AGY_BIN"] = bin
	ran := &[][]string{}
	runErr := new(error)
	env.RunCommand = func(cmd *exec.Cmd) error {
		*ran = append(*ran, cmd.Args)
		return *runErr
	}
	for _, name := range accounts {
		if _, err := (&provider.Antigravity{}).Add(env, []string{name}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewModel(env)
	m.execTerminal = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return fn(c.Run()) }
	}
	return m, env, ran, runErr
}

func press(t *testing.T, m Model, k tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(k)
	return next.(Model), cmd
}

func agyAccounts(t *testing.T, env *provider.Env) []provider.AccountEntry {
	t.Helper()
	entries, _, err := provider.Entries(env, &provider.Antigravity{}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestAddSignInProviderNeedsNameThenSignsIn(t *testing.T) {
	m, env, ran, _ := signInModel(t)
	m, _ = m.openSignIn(&provider.Antigravity{})

	m, cmd := press(t, m, keyType(tea.KeyEnter))
	if cmd != nil || m.dlg.kind != dialogAddName || !m.dlg.needName {
		t.Fatalf("empty name must keep the dialog open: kind=%v", m.dlg.kind)
	}
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("work")})
	m, cmd = press(t, m, keyType(tea.KeyEnter))
	if m.dlg.kind != dialogNone || cmd == nil {
		t.Fatalf("expected sign-in to start, kind=%v", m.dlg.kind)
	}
	m, _ = execCmd(m, cmd)
	if len(*ran) != 1 || strings.Join((*ran)[0][1:], " ") != "acp --login" {
		t.Fatalf("ran %q", *ran)
	}
	got := agyAccounts(t, env)
	if len(got) != 1 || got[0].Email != "work" || !got[0].Active {
		t.Fatalf("accounts = %+v", got)
	}
}

func TestSwitchSignInProviderChangesOnlyOnSuccess(t *testing.T) {
	m, env, ran, runErr := signInModel(t, "work", "personal")
	var target flatRow
	for _, r := range m.rows() {
		if r.provider.Key() == "antigravity" && !r.account.Active {
			target = r
		}
	}
	if target.account == nil {
		t.Fatal("expected an unmarked account")
	}
	before := agyAccounts(t, env)

	*runErr = errors.New("exit status 1")
	m, cmd := m.activateRow(target)
	m, _ = execCmd(m, cmd)
	if m.headerMsg.kind != msgError || !strings.Contains(m.headerMsg.text, "didn't finish") {
		t.Fatalf("header = %+v", m.headerMsg)
	}
	for i, a := range agyAccounts(t, env) {
		if a.Active != before[i].Active {
			t.Fatalf("failed sign-in changed the marked account: %+v", a)
		}
	}
	if len(*ran) != 1 {
		t.Fatalf("ran %q", *ran)
	}
}

func TestRotateAllSkipsSignInProvider(t *testing.T) {
	m, _, ran, _ := signInModel(t, "work", "personal")
	msg := rotateAllCmd(m.env, m.providers)().(actionDoneMsg)
	if len(*ran) != 0 || msg.ok || !strings.Contains(msg.text, "needs a sign-in") {
		t.Fatalf("rotate: ran=%q msg=%+v", *ran, msg)
	}
}

func TestRemoveSignInAccountOnlyForgetsName(t *testing.T) {
	m, _, _, _ := signInModel(t, "work")
	m = m.openRemoveDialog(&provider.Antigravity{}, m.data["antigravity"][0])
	var ids []string
	for _, c := range m.dialogChoices() {
		ids = append(ids, c.id)
	}
	if strings.Join(ids, ",") != "cancel,remove" || m.dlg.cursor != 0 {
		t.Fatalf("choices = %v cursor=%d", ids, m.dlg.cursor)
	}
}

func TestAddCanOnlySaveName(t *testing.T) {
	m, env, ran, _ := signInModel(t)
	m, _ = m.openSignIn(&provider.Antigravity{})
	// j and k are part of the name; only the arrows move between choices.
	m, _ = press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jk")})
	m, _ = press(t, m, keyType(tea.KeyDown))
	m, cmd := press(t, m, keyType(tea.KeyEnter))
	m, _ = execCmd(m, cmd)
	if len(*ran) != 0 {
		t.Fatalf("saving a name must not run agy: %q", *ran)
	}
	got := agyAccounts(t, env)
	if len(got) != 1 || got[0].Email != "jk" || !got[0].Active {
		t.Fatalf("accounts = %+v", got)
	}
}

func TestMissingAgyOffersInstallThenContinues(t *testing.T) {
	m, env, ran, _ := signInModel(t)
	bin := env.Getenv("TORI_AGY_BIN")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	m.env.RunCommand = func(cmd *exec.Cmd) error {
		*ran = append(*ran, cmd.Args)
		return store.WriteBytesAtomic(bin, []byte("stub"), 0o700, false)
	}

	m, _ = m.openSignIn(&provider.Antigravity{})
	if m.dlg.kind != dialogInstall || m.dlg.cursor != 0 {
		t.Fatalf("expected the install question, kind=%v", m.dlg.kind)
	}
	m, _ = press(t, m, keyType(tea.KeyEsc))
	if m.dlg.kind != dialogNone || len(*ran) != 0 {
		t.Fatalf("esc must not install: ran=%q", *ran)
	}

	m, _ = m.openSignIn(&provider.Antigravity{})
	m, cmd := press(t, m, keyType(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected the installer to start")
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if len(*ran) != 1 || !strings.Contains(strings.Join((*ran)[0], " "), "antigravity.google/cli/install.sh") {
		t.Fatalf("ran %q", *ran)
	}
	if m.dlg.kind != dialogAddName {
		t.Fatalf("expected the name prompt after installing, kind=%v", m.dlg.kind)
	}
}

func TestFailedInstallWaitsAndStops(t *testing.T) {
	m, env, _, runErr := signInModel(t)
	if err := os.Remove(env.Getenv("TORI_AGY_BIN")); err != nil {
		t.Fatal(err)
	}
	*runErr = errors.New("exit status 1")
	var out strings.Builder
	m.execTerminal = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		c.SetStdin(strings.NewReader("\n"))
		c.SetStdout(&out)
		return func() tea.Msg { return fn(c.Run()) }
	}
	m, _ = m.openSignIn(&provider.Antigravity{})
	m, cmd := press(t, m, keyType(tea.KeyEnter))
	next, _ := m.Update(cmd())
	m = next.(Model)
	if !strings.Contains(out.String(), "Press Enter to return to Tori.") {
		t.Fatalf("output = %q", out.String())
	}
	if m.dlg.kind != dialogNone || m.headerMsg.kind != msgError || !strings.Contains(m.headerMsg.text, "installer failed") {
		t.Fatalf("kind=%v header=%+v", m.dlg.kind, m.headerMsg)
	}
}

func TestSwitchWithoutAgyInstallsThenSignsIn(t *testing.T) {
	m, env, ran, _ := signInModel(t, "work", "personal")
	bin := env.Getenv("TORI_AGY_BIN")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	m.env.RunCommand = func(cmd *exec.Cmd) error {
		*ran = append(*ran, cmd.Args)
		return store.WriteBytesAtomic(bin, []byte("stub"), 0o700, false)
	}
	m, _ = m.activateRow(agyRow(t, m, false))
	if m.dlg.kind != dialogInstall {
		t.Fatalf("expected the install question, kind=%v", m.dlg.kind)
	}
	m, cmd := press(t, m, keyType(tea.KeyEnter))
	next, cmd := m.Update(cmd())
	m, _ = execCmd(next.(Model), cmd)
	if len(*ran) != 2 || strings.Join((*ran)[1][1:], " ") != "acp --login" {
		t.Fatalf("ran %q", *ran)
	}
	for _, a := range agyAccounts(t, env) {
		if a.Active != (a.Email == "work") {
			t.Fatalf("expected work to be marked: %+v", a)
		}
	}
}

func TestSignedInAccountCanSignInAgainOrOut(t *testing.T) {
	for _, tc := range []struct {
		downs       int
		args        string
		stillActive bool
	}{
		{0, "acp --login", true},
		{1, "-i /logout", false},
	} {
		m, env, ran, _ := signInModel(t, "work")
		m, _ = m.activateRow(agyRow(t, m, true))
		if m.dlg.kind != dialogAccount {
			t.Fatalf("expected the account dialog, kind=%v", m.dlg.kind)
		}
		for range tc.downs {
			m, _ = press(t, m, keyType(tea.KeyDown))
		}
		m, cmd := press(t, m, keyType(tea.KeyEnter))
		m, _ = execCmd(m, cmd)
		if len(*ran) != 1 || strings.Join((*ran)[0][1:], " ") != tc.args {
			t.Fatalf("ran %q, want %q", *ran, tc.args)
		}
		if got := agyAccounts(t, env); got[0].Active != tc.stillActive {
			t.Fatalf("after %q: %+v", tc.args, got)
		}
	}
}

func TestOldTokensCanBeDeletedFromPicker(t *testing.T) {
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true
	token := filepath.Join(store.SlotDir(env.Root, "antigravity", 1), "auth.json")
	if err := store.WriteJSONAtomic(token, map[string]any{"refreshToken": "rt"}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	m := NewModel(env)
	if m.headerMsg.kind != msgWarning || !strings.Contains(m.headerMsg.text, "press x") {
		t.Fatalf("header = %+v", m.headerMsg)
	}
	if footer, _ := m.renderFooter(); !strings.Contains(footer, "old tokens") {
		t.Fatalf("footer = %q", footer)
	}
	x := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}

	// Keeping is the default.
	m, _ = press(t, m, x)
	m, _ = press(t, m, keyType(tea.KeyEnter))
	if _, err := os.Stat(token); err != nil || m.dlg.kind != dialogNone {
		t.Fatalf("default choice must keep the file: %v", err)
	}

	m, _ = press(t, m, x)
	m, _ = press(t, m, keyType(tea.KeyDown))
	m, cmd := press(t, m, keyType(tea.KeyEnter))
	m, _ = execCmd(m, cmd)
	if _, err := os.Stat(token); !os.IsNotExist(err) {
		t.Fatalf("token still there: %v", err)
	}
	if footer, _ := m.renderFooter(); strings.Contains(footer, "old tokens") {
		t.Fatalf("footer = %q", footer)
	}
	if got := agyAccounts(t, env); len(got) != 1 {
		t.Fatalf("the account name must stay: %+v", got)
	}
}

func agyRow(t *testing.T, m Model, active bool) flatRow {
	t.Helper()
	for _, r := range m.rows() {
		if r.provider.Key() == "antigravity" && r.account != nil && r.account.Active == active {
			return r
		}
	}
	t.Fatalf("no Antigravity row with active=%v", active)
	return flatRow{}
}
