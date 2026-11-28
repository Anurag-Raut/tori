package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Anurag-Raut/tori/internal/provider"
)

// terminalAction runs a provider action that talks to the user's terminal, such
// as a sign-in. Bubble Tea releases the screen while Run executes.
type terminalAction struct {
	env provider.Env
	run func(env *provider.Env) error
}

func (a *terminalAction) SetStdin(r io.Reader)  { a.env.Stdin = r }
func (a *terminalAction) SetStdout(w io.Writer) { a.env.Stdout = w }
func (a *terminalAction) SetStderr(w io.Writer) { a.env.Stderr = w }
func (a *terminalAction) Run() error            { return a.run(&a.env) }

// signInCmd hands the terminal to the tool's sign-in and reports the outcome.
func (m Model) signInCmd(p provider.Provider, success string, run func(env *provider.Env) error) tea.Cmd {
	env := m.env
	return m.execTerminal(&terminalAction{env: *env, run: run}, func(err error) tea.Msg {
		msg := actionDoneMsg{ok: true, text: success, refresh: true, selectKey: p.Key(), terminal: true}
		if err != nil {
			msg.ok = false
			msg.text = err.Error()
		}
		if env.Now != nil {
			msg.now = env.Now()
		}
		return msg
	})
}

// openSignIn opens the add dialog for a provider that signs in by itself,
// offering to install its command first when it's missing.
func (m Model) openSignIn(p provider.SignInProvider) (Model, tea.Cmd) {
	if !p.Installed(m.env) {
		return m.openInstall(p, func(m Model) (Model, tea.Cmd) { return m.openSignIn(p) }), nil
	}
	m.dlg.close()
	m.dlg.kind = dialogAddName
	m.dlg.provider = p
	m.aliasInput.Reset()
	m.aliasInput.Placeholder = "e.g. work"
	return m, m.aliasInput.Focus()
}

// openInstall asks before running the installer; then runs after it succeeds.
func (m Model) openInstall(p provider.SignInProvider, then func(Model) (Model, tea.Cmd)) Model {
	m.dlg.close()
	m.dlg.kind = dialogInstall
	m.dlg.provider = p
	m.dlg.then = then
	return m
}

// installDoneMsg reports the installer's outcome.
type installDoneMsg struct {
	err  error
	then func(Model) (Model, tea.Cmd)
}

func (m Model) installCmd(p provider.SignInProvider, then func(Model) (Model, tea.Cmd)) tea.Cmd {
	action := &terminalAction{env: *m.env, run: func(env *provider.Env) error {
		_, err := p.Install(env)
		if err != nil {
			// The picker covers the installer's output, so let it be read.
			pauseForUser(env, err)
		}
		return err
	}}
	return m.execTerminal(action, func(err error) tea.Msg {
		return installDoneMsg{err: err, then: then}
	})
}

// pauseForUser shows err and waits for Enter before the picker returns.
func pauseForUser(env *provider.Env, err error) {
	if env.Stdin == nil || env.Stdout == nil {
		return
	}
	fmt.Fprintf(env.Stdout, "\n%v\nPress Enter to return to Tori.", err)
	buf := make([]byte, 1)
	for {
		n, rerr := env.Stdin.Read(buf)
		if rerr != nil || (n == 1 && (buf[0] == '\n' || buf[0] == '\r')) {
			return
		}
	}
}

func (m Model) handleInstallDone(msg installDoneMsg) (tea.Model, tea.Cmd) {
	m.loadLocalData()
	m.reconcileSelection()
	m.syncViewport()
	// Releasing the screen turned mouse reporting off.
	if msg.err != nil {
		return m, tea.Batch(tea.EnableMouseAllMotion, m.setHeaderMsg(msgError, msg.err.Error()))
	}
	header := m.setHeaderMsg(msgSuccess, "agy installed")
	var next tea.Cmd
	if msg.then != nil {
		m, next = msg.then(m)
	}
	if m.dlg.kind == dialogNone && next != nil {
		// The next step takes the terminal again and restores the mouse itself.
		return m, next
	}
	return m, tea.Batch(tea.EnableMouseAllMotion, header, next)
}

// openAccount offers sign-in and sign-out for the account marked signed in.
func (m Model) openAccount(p provider.SignInProvider, acct provider.AccountEntry) Model {
	m.dlg.close()
	m.dlg.kind = dialogAccount
	m.dlg.provider = p
	m.dlg.account = &acct
	return m
}

// legacyProvider returns the provider with old token files on disk, or nil.
func (m Model) legacyProvider() provider.SignInProvider {
	for _, p := range m.providers {
		if sp, ok := p.(provider.SignInProvider); ok && len(m.legacy[p.Key()]) > 0 {
			return sp
		}
	}
	return nil
}

func (m Model) openCleanup() Model {
	p := m.legacyProvider()
	if p == nil {
		return m
	}
	m.dlg.close()
	m.dlg.kind = dialogCleanup
	m.dlg.provider = p
	m.dlg.files = m.legacy[p.Key()]
	return m
}

func deleteLegacyCmd(env *provider.Env, p provider.SignInProvider) tea.Cmd {
	return func() tea.Msg {
		n, err := p.DeleteLegacyTokens(env)
		msg := actionDoneMsg{ok: true, text: fmt.Sprintf("Deleted %d old token files", n), refresh: true}
		if n == 1 {
			msg.text = "Deleted 1 old token file"
		}
		if err != nil {
			msg.ok = false
			msg.text = err.Error()
		}
		if env.Now != nil {
			msg.now = env.Now()
		}
		return msg
	}
}

// saveNameCmd records name as the signed-in account without a new sign-in.
func saveNameCmd(env *provider.Env, p provider.SignInProvider, name string) tea.Cmd {
	return func() tea.Msg {
		_, err := p.Add(env, []string{name})
		msg := actionDoneMsg{ok: true, text: fmt.Sprintf("Saved %s as signed in", name), refresh: true, selectKey: p.Key()}
		if err != nil {
			msg.ok = false
			msg.text = err.Error()
		}
		if env.Now != nil {
			msg.now = env.Now()
		}
		return msg
	}
}

// handleAddNameKey types the name, with the arrows choosing what to do with it.
func (m Model) handleAddNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	choices := m.dialogChoices()
	switch {
	case msg.Type == tea.KeyDown:
		m.dlg.cursor = (m.dlg.cursor + 1) % len(choices)
		return m, nil
	case msg.Type == tea.KeyUp:
		m.dlg.cursor = (m.dlg.cursor - 1 + len(choices)) % len(choices)
		return m, nil
	case key.Matches(msg, m.keys.Switch):
		return m.executeDialogChoice(choices[m.dlg.cursor].id)
	}
	var cmd tea.Cmd
	m.aliasInput, cmd = m.aliasInput.Update(msg)
	return m, cmd
}

// submitName signs in as, or just saves, the typed account name.
func (m Model) submitName(id string) (tea.Model, tea.Cmd) {
	p, ok := m.dlg.provider.(provider.SignInProvider)
	if !ok {
		m.dlg.close()
		return m, nil
	}
	name := strings.TrimSpace(m.aliasInput.Value())
	if name == "" {
		m.dlg.needName = true
		return m, nil
	}
	m.aliasInput.Blur()
	m.dlg.close()
	if id == "save" {
		return m, saveNameCmd(m.env, p, name)
	}
	return m.withTargets(p, "Sign in Antigravity as "+name, func(m Model, ids []string) (Model, tea.Cmd) {
		return m, m.signInCmd(p, fmt.Sprintf("Signed in to %s", name), func(env *provider.Env) error {
			env.Targets = ids
			_, err := p.Login(env, name)
			return err
		})
	})
}

// withTargets runs act with the Antigravity targets to sign in to. With several
// targets it asks first; with one it just goes (nil means the default choice).
func (m Model) withTargets(p provider.SignInProvider, title string, act func(Model, []string) (Model, tea.Cmd)) (Model, tea.Cmd) {
	tp, ok := p.(provider.TargetedProvider)
	if !ok {
		return act(m, nil)
	}
	targets := tp.Targets(m.env)
	if len(targets) <= 1 {
		return act(m, nil)
	}
	remembered := tp.Remembered(m.env)
	m.dlg.close()
	m.dlg.kind = dialogTargets
	m.dlg.provider = p
	m.dlg.targets = targets
	m.dlg.title = title
	m.dlg.pick = act
	m.dlg.checked = make([]bool, len(targets))
	for i, t := range targets {
		for _, id := range remembered {
			if id == t.ID {
				m.dlg.checked[i] = true
			}
		}
	}
	return m, nil
}

func (m Model) toggleTarget() Model {
	checked := append([]bool(nil), m.dlg.checked...)
	if i := m.dlg.cursor; i >= 0 && i < len(checked) {
		checked[i] = !checked[i]
	}
	m.dlg.checked = checked
	m.dlg.needTarget = false
	return m
}

// handleTargetsKey moves, toggles with space, and starts with Enter.
func (m Model) handleTargetsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.dlg.targets)
	switch {
	case msg.Type == tea.KeySpace || msg.String() == " ":
		return m.toggleTarget(), nil
	case key.Matches(msg, m.keys.Down):
		m.dlg.cursor = (m.dlg.cursor + 1) % n
	case key.Matches(msg, m.keys.Up):
		m.dlg.cursor = (m.dlg.cursor - 1 + n) % n
	case key.Matches(msg, m.keys.Switch):
		var ids []string
		for i, t := range m.dlg.targets {
			if m.dlg.checked[i] {
				ids = append(ids, t.ID)
			}
		}
		if len(ids) == 0 {
			m.dlg.needTarget = true
			return m, nil
		}
		pick := m.dlg.pick
		m.dlg.close()
		return pick(m, ids)
	}
	return m, nil
}
