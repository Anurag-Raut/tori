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

// openSignIn opens the add dialog for a provider that signs in by itself.
func (m Model) openSignIn(p provider.SignInProvider) (Model, tea.Cmd) {
	m.dlg.close()
	m.dlg.kind = dialogAddName
	m.dlg.provider = p
	m.dlg.notInstalled = !p.Installed(m.env)
	m.aliasInput.Reset()
	m.aliasInput.Placeholder = "e.g. work"
	return m, m.aliasInput.Focus()
}

func (m Model) handleAddNameKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	enter := key.Matches(msg, m.keys.Switch)
	p, ok := m.dlg.provider.(provider.SignInProvider)
	if !ok || m.dlg.notInstalled {
		if enter {
			m.aliasInput.Blur()
			m.dlg.close()
		}
		return m, nil
	}
	if !enter {
		var cmd tea.Cmd
		m.aliasInput, cmd = m.aliasInput.Update(msg)
		return m, cmd
	}
	name := strings.TrimSpace(m.aliasInput.Value())
	if name == "" {
		m.dlg.needName = true
		return m, nil
	}
	m.aliasInput.Blur()
	m.dlg.close()
	return m, m.signInCmd(p, fmt.Sprintf("Signed in to %s", name), func(env *provider.Env) error {
		_, err := p.Login(env, name)
		return err
	})
}
