package tui

import (
	"errors"
	"os/exec"
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

func TestOldTokenWarningOnStart(t *testing.T) {
	env, _ := setupTestEnv(t, nil)
	env.DisableNetwork = true
	dir := store.SlotDir(env.Root, "antigravity", 1)
	if err := store.WriteJSONAtomic(dir+"/auth.json", map[string]any{"refreshToken": "rt"}, 0o600, false); err != nil {
		t.Fatal(err)
	}
	m := NewModel(env)
	if m.headerMsg.kind != msgWarning || !strings.Contains(m.headerMsg.text, "tori g cleanup") {
		t.Fatalf("header = %+v", m.headerMsg)
	}
}
