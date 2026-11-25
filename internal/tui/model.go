package tui

import (
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
)

type modalKind int

const (
	modalNone modalKind = iota
	modalAdd
	modalDelete
	modalDisclaimer
)

type addStep int

const (
	addStepGuide addStep = iota
	addStepSave
)

type msgTone int

const (
	toneNormal msgTone = iota
	toneGreen
	toneYellow
	toneRed
)

type flatRow struct {
	provider provider.Provider
	account  *provider.AccountEntry // nil for an empty or errored provider placeholder
}

type usageResult struct {
	key      string
	accounts []provider.AccountEntry
	err      string
}

type usageLoadedMsg struct {
	results []usageResult
	note    string
}

type actionDoneMsg struct {
	ok        bool
	text      string
	refresh   bool
	selectKey string
	selectNum int
}

type keyItem struct {
	key       string
	label     string
	actionKey string
}

type hitRect struct {
	id     string
	x0, x1 int
	y      int
}

type boxRect struct {
	x0, y0, x1, y1 int
}

var footerKeys = []keyItem{
	{"↑↓", "move", ""},
	{"⏎", "switch", "enter"},
	{"⇥", "tool", "tab"},
	{"a", "add", "a"},
	{"d", "remove", "d"},
	{"u", "usage", "u"},
	{"b", "rotate", "b"},
	{"r", "refresh", "r"},
	{"q", "quit", "q"},
}

var eighths = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}

// Model is the Bubble Tea model for the Tori interactive TUI.
type Model struct {
	env       *provider.Env
	providers []provider.Provider
	data      map[string][]provider.AccountEntry
	errors    map[string]string
	usageOn   map[string]bool

	cur    int
	offset int
	width  int
	height int

	statusMsg  string
	statusTone msgTone
	loading    bool

	modal         modalKind
	modalProvider provider.Provider
	modalAccount  *provider.AccountEntry
	addStep       addStep
	addStatus     provider.StatusInfo
	addNotice     string
	addNoticeErr  bool
	aliasInput    textinput.Model

	spinner spinner.Model
	styles  palette

	hoverRow      int
	hoverFooter   string
	hoverModalBtn string
	lastClickRow  int
	lastClickAt   time.Time
}

// NewModel initializes the TUI model synchronously from local disk/cache (zero network calls).
func NewModel(env *provider.Env) Model {
	if env == nil {
		env = provider.NewEnv()
	}
	// Clone env so TUI never prints raw warnings over the alt screen.
	tuiEnv := *env
	tuiEnv.Warn = func(string) {}

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	ti := textinput.New()
	ti.Placeholder = "optional alias (e.g. work)"
	ti.CharLimit = 40
	ti.Width = 36

	m := Model{
		env:          &tuiEnv,
		providers:    provider.All(),
		data:         make(map[string][]provider.AccountEntry),
		errors:       make(map[string]string),
		usageOn:      make(map[string]bool),
		width:        100,
		height:       30,
		aliasInput:   ti,
		spinner:      sp,
		styles:       newPalette(),
		hoverRow:     -1,
		lastClickRow: -1,
	}
	m.styles.spinnerApply(&m.spinner)
	m.loadLocalData()

	// Start cursor on the first active account if any.
	rows := m.rows()
	for i, r := range rows {
		if r.account != nil && r.account.Active {
			m.cur = i
			break
		}
	}
	return m
}

func (p palette) spinnerApply(sp *spinner.Model) {
	sp.Style = p.accent
}

func (m *Model) loadLocalData() {
	for _, p := range m.providers {
		m.usageOn[p.Key()] = m.env.IsUsageEnabled(p.Key())
		entries, _, err := provider.Entries(m.env, p, false, false)
		if err != nil {
			m.data[p.Key()] = nil
			m.errors[p.Key()] = err.Error()
		} else {
			m.data[p.Key()] = entries
			m.errors[p.Key()] = ""
		}
	}
}

func (m Model) shownProviders() []provider.Provider {
	counts := make(map[string]int, len(m.providers))
	for _, p := range m.providers {
		counts[p.Key()] = len(m.data[p.Key()])
	}
	return provider.Ordered(m.providers, counts)
}

func (m Model) rows() []flatRow {
	var out []flatRow
	for _, p := range m.shownProviders() {
		accts := m.data[p.Key()]
		if len(accts) == 0 || m.errors[p.Key()] != "" {
			out = append(out, flatRow{provider: p, account: nil})
			continue
		}
		for i := range accts {
			acct := accts[i]
			out = append(out, flatRow{provider: p, account: &acct})
		}
	}
	return out
}

func (m Model) anyUsageEnabled() bool {
	for _, p := range m.providers {
		if m.usageOn[p.Key()] {
			return true
		}
	}
	return false
}

// Init kicks off async usage fetching if any provider has usage meters enabled.
func (m Model) Init() tea.Cmd {
	if !m.anyUsageEnabled() {
		return nil
	}
	return tea.Batch(
		m.spinner.Tick,
		fetchUsageCmd(m.env, m.providers, false, "", ""),
	)
}

func fetchUsageCmd(env *provider.Env, providers []provider.Provider, force bool, onlyKey string, note string) tea.Cmd {
	return func() tea.Msg {
		targets := make([]provider.Provider, 0, len(providers))
		for _, p := range providers {
			if onlyKey != "" && p.Key() != onlyKey {
				continue
			}
			if env.IsUsageEnabled(p.Key()) {
				targets = append(targets, p)
			}
		}
		if len(targets) == 0 {
			return usageLoadedMsg{note: note}
		}
		results := make([]usageResult, len(targets))
		var wg sync.WaitGroup
		for i, p := range targets {
			wg.Add(1)
			go func(idx int, prov provider.Provider) {
				defer wg.Done()
				entries, _, err := provider.Entries(env, prov, true, force)
				res := usageResult{key: prov.Key(), accounts: entries}
				if err != nil {
					res.err = err.Error()
				}
				results[idx] = res
			}(i, p)
		}
		wg.Wait()
		return usageLoadedMsg{results: results, note: note}
	}
}

func switchCmd(env *provider.Env, p provider.Provider, ref string) tea.Cmd {
	return func() tea.Msg {
		var warnBuf []string
		e := *env
		e.Warn = func(msg string) { warnBuf = append(warnBuf, msg) }
		out, err := provider.Switch(&e, p, ref)
		if err != nil {
			return actionDoneMsg{ok: false, text: fmt.Sprintf("%s: %s", p.Title(), err.Error())}
		}
		msg := ""
		if len(out) > 0 {
			msg = out[0]
		}
		if len(warnBuf) > 0 && msg != "" {
			msg = msg + " (" + warnBuf[0] + ")"
		}
		return actionDoneMsg{
			ok:        true,
			text:      fmt.Sprintf("%s: %s", p.Title(), msg),
			refresh:   true,
			selectKey: p.Key(),
		}
	}
}

func addCmd(env *provider.Env, p provider.Provider, alias string) tea.Cmd {
	return func() tea.Msg {
		var args []string
		if alias = strings.TrimSpace(alias); alias != "" {
			args = []string{alias}
		}
		out, err := p.Add(env, args)
		if err != nil {
			return actionDoneMsg{ok: false, text: fmt.Sprintf("%s: %s", p.Title(), err.Error())}
		}
		msg := ""
		if len(out) > 0 {
			msg = out[0]
		}
		return actionDoneMsg{
			ok:        true,
			text:      fmt.Sprintf("%s: %s", p.Title(), msg),
			refresh:   true,
			selectKey: p.Key(),
		}
	}
}

func removeCmd(env *provider.Env, p provider.Provider, num int, meta provider.AccountEntry) tea.Cmd {
	return func() tea.Msg {
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error()}
		}
		for _, s := range slots {
			if s.Number == num {
				msg, err := provider.Remove(env, p, s)
				if err != nil {
					return actionDoneMsg{ok: false, text: err.Error()}
				}
				return actionDoneMsg{ok: true, text: msg, refresh: true}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d (%s) no longer exists.", num, meta.Email)}
	}
}

func switchAndRemoveCmd(env *provider.Env, p provider.Provider, switchTo int, removeNum int, meta provider.AccountEntry) tea.Cmd {
	return func() tea.Msg {
		e := *env
		e.Warn = func(string) {}
		out, err := provider.Switch(&e, p, fmt.Sprintf("%d", switchTo))
		if err != nil {
			return actionDoneMsg{ok: false, text: fmt.Sprintf("%s: %s", p.Title(), err.Error())}
		}
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), refresh: true}
		}
		for _, s := range slots {
			if s.Number == removeNum {
				if _, err := provider.Remove(env, p, s); err != nil {
					return actionDoneMsg{ok: false, text: err.Error(), refresh: true}
				}
				switched := ""
				if len(out) > 0 {
					switched = out[0]
				}
				return actionDoneMsg{
					ok:      true,
					text:    fmt.Sprintf("%s: %s — removed %s", p.Title(), switched, meta.Email),
					refresh: true,
				}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d (%s) no longer exists.", removeNum, meta.Email), refresh: true}
	}
}

func removeAndClearCmd(env *provider.Env, p provider.Provider, num int, meta provider.AccountEntry) tea.Cmd {
	return func() tea.Msg {
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error()}
		}
		for _, s := range slots {
			if s.Number == num {
				if _, err := provider.Remove(env, p, s); err != nil {
					return actionDoneMsg{ok: false, text: err.Error()}
				}
				if _, err := p.ClearLive(env); err != nil {
					return actionDoneMsg{ok: false, text: fmt.Sprintf("Removed slot %d, but failed to clear local login: %v", num, err), refresh: true}
				}
				return actionDoneMsg{
					ok:      true,
					text:    fmt.Sprintf("%s: removed %s and cleared local login", p.Title(), meta.Email),
					refresh: true,
				}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d (%s) no longer exists.", num, meta.Email)}
	}
}

func rotateAllCmd(env *provider.Env, providers []provider.Provider) tea.Cmd {
	return func() tea.Msg {
		e := *env
		e.Warn = func(string) {}
		var msgs []string
		okAll := true
		rotated := 0
		for _, p := range providers {
			slots, _ := p.Slots(&e)
			if len(slots) < 2 {
				continue
			}
			rotated++
			out, err := provider.Switch(&e, p, "")
			if err != nil {
				okAll = false
				msgs = append(msgs, fmt.Sprintf("%s: %s", p.Title(), err.Error()))
			} else if len(out) > 0 {
				msgs = append(msgs, fmt.Sprintf("%s: %s", p.Title(), out[0]))
			}
		}
		if rotated == 0 {
			return actionDoneMsg{ok: false, text: "Need at least 2 accounts in a tool to rotate."}
		}
		return actionDoneMsg{
			ok:      okAll,
			text:    strings.Join(msgs, " | "),
			refresh: true,
		}
	}
}

// Update handles Bubble Tea messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.syncScroll()
		return m, nil

	case spinner.TickMsg:
		if !m.loading && m.statusMsg == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case usageLoadedMsg:
		m.loading = false
		for _, res := range msg.results {
			if res.err != "" {
				m.errors[res.key] = res.err
				m.data[res.key] = nil
			} else {
				m.errors[res.key] = ""
				m.data[res.key] = res.accounts
			}
		}
		if msg.note != "" {
			m.statusMsg = msg.note
			m.statusTone = toneGreen
		} else if strings.HasPrefix(m.statusMsg, "Reading") || strings.HasPrefix(m.statusMsg, "Refreshing") {
			m.statusMsg = ""
		}
		m.clampCursor()
		m.syncScroll()
		return m, nil

	case actionDoneMsg:
		if msg.refresh {
			m.loadLocalData()
			m.clampCursor()
			m.syncScroll()
		}
		m.statusMsg = msg.text
		if msg.ok {
			m.statusTone = toneGreen
		} else {
			m.statusTone = toneRed
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.modal != modalNone {
			return m.handleModalKey(msg)
		}
		return m.handleMainKey(msg)
	}
	return m, nil
}

func (m Model) nextOtherAccount(pKey string, excludeNum int) *provider.AccountEntry {
	accts := m.data[pKey]
	if len(accts) < 2 {
		return nil
	}
	// Prefer the next slot after excludeNum, wrapping around.
	for _, a := range accts {
		if a.Number > excludeNum {
			cp := a
			return &cp
		}
	}
	for _, a := range accts {
		if a.Number != excludeNum {
			cp := a
			return &cp
		}
	}
	return nil
}

func (m Model) cancelAddModal() (Model, tea.Cmd) {
	m.modal = modalNone
	m.hoverModalBtn = ""
	m.aliasInput.Blur()
	m.addNotice = ""
	m.addNoticeErr = false
	m.statusMsg = "Cancelled."
	m.statusTone = toneYellow
	return m, nil
}

func (m Model) recheckAddStatus() (Model, tea.Cmd) {
	p := m.modalProvider
	st, _ := p.Status(m.env)
	m.addStatus = st
	m.loadLocalData()
	if st.LoggedIn && st.ManagedSlot == nil {
		m.addStep = addStepSave
		m.addNotice = ""
		m.addNoticeErr = false
		m.aliasInput.Reset()
		cmd := m.aliasInput.Focus()
		return m, cmd
	}
	if st.LoggedIn && st.ManagedSlot != nil {
		m.addNotice = fmt.Sprintf("Still signed in as %s (already slot %d). Log out and sign in to a different account first.", st.DisplayName, *st.ManagedSlot)
		m.addNoticeErr = false
		return m, nil
	}
	m.addNotice = "No signed-in account found yet. Sign in first, then press r (Re-check)."
	m.addNoticeErr = false
	return m, nil
}

func (m Model) cycleAddProvider(delta int) (Model, tea.Cmd) {
	if len(m.providers) == 0 {
		return m, nil
	}
	idx := 0
	if m.modalProvider != nil {
		for i, prov := range m.providers {
			if prov.Key() == m.modalProvider.Key() {
				idx = i
				break
			}
		}
	}
	nextIdx := (idx + delta + len(m.providers)) % len(m.providers)
	return m.openAddModal(m.providers[nextIdx])
}

func (m Model) triggerModalAction(id string) (tea.Model, tea.Cmd) {
	m.hoverModalBtn = ""
	switch m.modal {
	case modalAdd:
		if key, ok := strings.CutPrefix(id, "tab:"); ok {
			for _, prov := range m.providers {
				if prov.Key() == key {
					return m.openAddModal(prov)
				}
			}
			return m, nil
		}
		if m.addStep == addStepSave {
			switch id {
			case "cancel":
				return m.cancelAddModal()
			case "save":
				alias := m.aliasInput.Value()
				p := m.modalProvider
				m.modal = modalNone
				m.aliasInput.Blur()
				m.addNotice = ""
				m.addNoticeErr = false
				m.statusMsg = "Saving account..."
				m.statusTone = toneYellow
				return m, addCmd(m.env, p, alias)
			}
			return m, nil
		}

		switch id {
		case "cancel":
			return m.cancelAddModal()
		case "recheck":
			return m.recheckAddStatus()
		case "update":
			if m.addStatus.ManagedSlot != nil {
				m.addStep = addStepSave
				m.addNotice = ""
				m.addNoticeErr = false
				m.aliasInput.Reset()
				cmd := m.aliasInput.Focus()
				return m, cmd
			}
		}

	case modalDelete:
		switch id {
		case "remove":
			p := m.modalProvider
			acct := *m.modalAccount
			m.modal = modalNone
			m.modalAccount = nil
			return m, removeCmd(m.env, p, acct.Number, acct)
		case "switch_remove":
			if m.modalAccount != nil && m.modalAccount.Active {
				if other := m.nextOtherAccount(m.modalProvider.Key(), m.modalAccount.Number); other != nil {
					p := m.modalProvider
					acct := *m.modalAccount
					m.modal = modalNone
					m.modalAccount = nil
					m.statusMsg = "Switching & removing..."
					m.statusTone = toneYellow
					return m, switchAndRemoveCmd(m.env, p, other.Number, acct.Number, acct)
				}
			}
		case "remove_clear":
			if m.modalAccount != nil && m.modalAccount.Active && len(m.data[m.modalProvider.Key()]) == 1 {
				p := m.modalProvider
				acct := *m.modalAccount
				m.modal = modalNone
				m.modalAccount = nil
				m.statusMsg = "Removing & clearing local login..."
				m.statusTone = toneYellow
				return m, removeAndClearCmd(m.env, p, acct.Number, acct)
			}
		case "cancel":
			m.modal = modalNone
			m.modalAccount = nil
			m.statusMsg = "Cancelled."
			m.statusTone = toneYellow
			return m, nil
		}

	case modalDisclaimer:
		switch id {
		case "accept":
			p := m.modalProvider
			m.modal = modalNone
			if err := config.SetUsage(m.env.Root, []string{p.Key()}, true); err != nil {
				m.statusMsg = err.Error()
				m.statusTone = toneRed
				return m, nil
			}
			m.usageOn[p.Key()] = true
			m.loadLocalData()
			m.loading = true
			m.statusMsg = fmt.Sprintf("%s: usage meters on, reading usage…", p.Title())
			m.statusTone = toneYellow
			return m, tea.Batch(
				m.spinner.Tick,
				fetchUsageCmd(m.env, m.providers, false, p.Key(), fmt.Sprintf("%s: usage meters on", p.Title())),
			)
		case "cancel":
			m.modal = modalNone
			m.statusMsg = "Cancelled."
			m.statusTone = toneYellow
			return m, nil
		}
	}
	return m, nil
}

func (m Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.modal {
	case modalAdd:
		if m.addStep == addStepSave {
			switch msg.String() {
			case "esc":
				return m.triggerModalAction("cancel")
			case "enter":
				return m.triggerModalAction("save")
			case "tab":
				return m.cycleAddProvider(1)
			case "shift+tab":
				return m.cycleAddProvider(-1)
			case "left":
				if m.aliasInput.Value() == "" {
					return m.cycleAddProvider(-1)
				}
				var cmd tea.Cmd
				m.aliasInput, cmd = m.aliasInput.Update(msg)
				return m, cmd
			case "right":
				if m.aliasInput.Value() == "" {
					return m.cycleAddProvider(1)
				}
				var cmd tea.Cmd
				m.aliasInput, cmd = m.aliasInput.Update(msg)
				return m, cmd
			default:
				var cmd tea.Cmd
				m.aliasInput, cmd = m.aliasInput.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "esc":
			return m.triggerModalAction("cancel")
		case "left", "h", "shift+tab":
			return m.cycleAddProvider(-1)
		case "right", "l", "tab":
			return m.cycleAddProvider(1)
		case "r", "R", "enter":
			return m.triggerModalAction("recheck")
		case "s", "S":
			return m.triggerModalAction("update")
		}

	case modalDelete:
		switch msg.String() {
		case "y", "Y":
			return m.triggerModalAction("remove")
		case "s", "S":
			return m.triggerModalAction("switch_remove")
		case "c", "C":
			return m.triggerModalAction("remove_clear")
		case "n", "N", "esc":
			return m.triggerModalAction("cancel")
		}

	case modalDisclaimer:
		switch msg.String() {
		case "y", "Y", "enter":
			return m.triggerModalAction("accept")
		case "n", "N", "esc":
			return m.triggerModalAction("cancel")
		}
	}
	return m, nil
}

func (m Model) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	flat := m.rows()
	n := len(flat)

	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit

	case "down", "j":
		if n > 0 {
			m.cur = (m.cur + 1) % n
			m.syncScroll()
		}
		return m, nil

	case "up", "k":
		if n > 0 {
			m.cur = (m.cur - 1 + n) % n
			m.syncScroll()
		}
		return m, nil

	case "tab":
		if n > 0 {
			shown := m.shownProviders()
			curKey := flat[m.cur].provider.Key()
			curIdx := 0
			for i, p := range shown {
				if p.Key() == curKey {
					curIdx = i
					break
				}
			}
			targetKey := shown[(curIdx+1)%len(shown)].Key()
			for i, r := range flat {
				if r.provider.Key() == targetKey {
					m.cur = i
					break
				}
			}
			m.syncScroll()
		}
		return m, nil

	case "enter":
		if n == 0 {
			return m, nil
		}
		row := flat[m.cur]
		if row.account == nil {
			return m.openAddModal(row.provider)
		}
		if row.account.Active {
			m.statusMsg = fmt.Sprintf("%s: already on %s", row.provider.Title(), row.account.Email)
			m.statusTone = toneYellow
			return m, nil
		}
		m.statusMsg = "Switching..."
		m.statusTone = toneYellow
		return m, switchCmd(m.env, row.provider, fmt.Sprintf("%d", row.account.Number))

	case "a":
		if n == 0 {
			return m, nil
		}
		return m.openAddModal(flat[m.cur].provider)

	case "d", "delete":
		if n == 0 {
			return m, nil
		}
		row := flat[m.cur]
		if row.account == nil {
			m.statusMsg = "Nothing to remove here."
			m.statusTone = toneYellow
			return m, nil
		}
		acctCopy := *row.account
		m.modal = modalDelete
		m.modalProvider = row.provider
		m.modalAccount = &acctCopy
		m.hoverModalBtn = ""
		return m, nil

	case "u":
		if n == 0 {
			return m, nil
		}
		p := flat[m.cur].provider
		if m.usageOn[p.Key()] {
			if err := config.SetUsage(m.env.Root, []string{p.Key()}, false); err != nil {
				m.statusMsg = err.Error()
				m.statusTone = toneRed
				return m, nil
			}
			m.usageOn[p.Key()] = false
			m.loadLocalData()
			m.statusMsg = fmt.Sprintf("%s: usage meters off", p.Title())
			m.statusTone = toneYellow
			return m, nil
		}
		m.modal = modalDisclaimer
		m.modalProvider = p
		m.hoverModalBtn = ""
		return m, nil

	case "b":
		m.statusMsg = "Rotating accounts..."
		m.statusTone = toneYellow
		return m, rotateAllCmd(m.env, m.providers)

	case "r":
		if !m.anyUsageEnabled() {
			m.statusMsg = "Usage meters are off; press u to turn them on."
			m.statusTone = toneYellow
			return m, nil
		}
		m.loading = true
		m.statusMsg = "Refreshing usage..."
		m.statusTone = toneYellow
		return m, tea.Batch(
			m.spinner.Tick,
			fetchUsageCmd(m.env, m.providers, true, "", ""),
		)
	}
	return m, nil
}

func (m Model) openAddModal(p provider.Provider) (Model, tea.Cmd) {
	st, _ := p.Status(m.env)
	m.modal = modalAdd
	m.modalProvider = p
	m.addStatus = st
	m.addNotice = ""
	m.addNoticeErr = false
	m.hoverModalBtn = ""
	m.aliasInput.Reset()
	if p.Key() == "antigravity" {
		m.aliasInput.Placeholder = "alias (optional, e.g. primary)"
	} else {
		m.aliasInput.Placeholder = "optional alias (Enter for none)"
	}
	if st.LoggedIn && st.ManagedSlot == nil {
		m.addStep = addStepSave
		cmd := m.aliasInput.Focus()
		return m, cmd
	}
	m.addStep = addStepGuide
	m.aliasInput.Blur()
	return m, nil
}

func keyFromAction(action string) tea.KeyMsg {
	switch action {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(action)}
	}
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Modal mouse hover & click handling.
	if m.modal != modalNone {
		_, hits, boxR := m.buildModal()
		switch {
		case msg.Action == tea.MouseActionMotion:
			hovered := ""
			for _, h := range hits {
				if msg.Y == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					hovered = h.id
					break
				}
			}
			m.hoverModalBtn = hovered
			return m, nil

		case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
			for _, h := range hits {
				if msg.Y == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					return m.triggerModalAction(h.id)
				}
			}
			// Clicking outside the modal box cancels cleanly (like Esc).
			if msg.X < boxR.x0 || msg.X >= boxR.x1 || msg.Y < boxR.y0 || msg.Y >= boxR.y1 {
				return m.triggerModalAction("cancel")
			}
		}
		return m, nil
	}

	flat := m.rows()
	n := len(flat)
	if n == 0 {
		return m, nil
	}

	// Hover tracking across rows and clickable footer pills.
	if msg.Action == tea.MouseActionMotion {
		layout := m.computeLayout()
		bodyH := m.bodyHeight(layout.footerHeight)
		footerTopY := bodyH + 1

		m.hoverRow = -1
		m.hoverFooter = ""
		if msg.Y >= 0 && msg.Y < bodyH {
			bodyLine := msg.Y + m.offset
			if rowIdx, ok := layout.lineToRow[bodyLine]; ok && rowIdx >= 0 && rowIdx < n {
				m.hoverRow = rowIdx
			}
		} else if msg.Y >= footerTopY {
			fy := msg.Y - footerTopY
			for _, h := range layout.footerHits {
				if fy == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					m.hoverFooter = h.id
					break
				}
			}
		}
		return m, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action == tea.MouseActionPress {
			m.cur = (m.cur - 1 + n) % n
			m.syncScroll()
		}
		return m, nil

	case tea.MouseButtonWheelDown:
		if msg.Action == tea.MouseActionPress {
			m.cur = (m.cur + 1) % n
			m.syncScroll()
		}
		return m, nil

	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		layout := m.computeLayout()
		bodyH := m.bodyHeight(layout.footerHeight)
		footerTopY := bodyH + 1

		// Click on a footer action button.
		if msg.Y >= footerTopY {
			fy := msg.Y - footerTopY
			for _, h := range layout.footerHits {
				if fy == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					return m.handleMainKey(keyFromAction(h.id))
				}
			}
			return m, nil
		}

		if msg.Y < 0 || msg.Y >= bodyH {
			return m, nil
		}
		bodyLine := msg.Y + m.offset
		rowIdx, ok := layout.lineToRow[bodyLine]
		if !ok || rowIdx < 0 || rowIdx >= n {
			return m, nil
		}
		now := m.now()
		isDouble := m.lastClickRow == rowIdx && now.Sub(m.lastClickAt) < 450*time.Millisecond
		m.cur = rowIdx
		m.hoverRow = rowIdx
		m.lastClickRow = rowIdx
		m.lastClickAt = now
		m.syncScroll()

		if isDouble {
			row := flat[rowIdx]
			if row.account == nil {
				return m.openAddModal(row.provider)
			}
			if row.account.Active {
				m.statusMsg = fmt.Sprintf("%s: already on %s", row.provider.Title(), row.account.Email)
				m.statusTone = toneYellow
				return m, nil
			}
			m.statusMsg = "Switching..."
			m.statusTone = toneYellow
			return m, switchCmd(m.env, row.provider, fmt.Sprintf("%d", row.account.Number))
		}
	}
	return m, nil
}

func (m Model) now() time.Time {
	if m.env != nil && m.env.Now != nil {
		return m.env.Now()
	}
	return time.Now()
}

func (m *Model) clampCursor() {
	n := len(m.rows())
	if n == 0 {
		m.cur = 0
		return
	}
	if m.cur < 0 {
		m.cur = 0
	}
	if m.cur >= n {
		m.cur = n - 1
	}
}

func (m *Model) syncScroll() {
	layout := m.computeLayout()
	bodyH := m.bodyHeight(layout.footerHeight)
	if bodyH <= 0 || len(layout.lines) <= bodyH {
		m.offset = 0
		return
	}
	selLine := 0
	if m.cur >= 0 && m.cur < len(layout.rowToLine) {
		selLine = layout.rowToLine[m.cur]
	}
	off := selLine - bodyH/2
	if off < 0 {
		off = 0
	}
	maxOff := len(layout.lines) - bodyH
	if off > maxOff {
		off = maxOff
	}
	if off < 3 {
		off = 0
	}
	m.offset = off
}

func (m Model) bodyHeight(footerH int) int {
	h := m.height - 1 - footerH
	if h < 1 {
		return 1
	}
	return h
}

type layoutResult struct {
	lines        []string
	rowToLine    []int
	lineToRow    map[int]int
	footerLines  []string
	footerHits   []hitRect
	footerHeight int
}

func (m Model) renderFooter() ([]string, []hitRect) {
	w := m.width
	gap := 2
	if w < 90 {
		gap = 1
	}
	var rows []string
	var hits []hitRect
	var cur strings.Builder
	cur.WriteString(" ")
	x := 1
	rowIdx := 0
	for _, item := range footerKeys {
		hovered := item.actionKey != "" && item.actionKey == m.hoverFooter
		capStyle := m.styles.keycap
		labStyle := m.styles.muted
		if hovered {
			capStyle = m.styles.keycapHover
			labStyle = m.styles.bold
		}
		capStr := capStyle.Render(item.key)
		labStr := " " + labStyle.Render(item.label)
		itemStr := capStr + labStr
		need := lipgloss.Width(itemStr)
		if x+need > w-1 && x > 1 {
			rows = append(rows, cur.String())
			cur.Reset()
			cur.WriteString(" ")
			x = 1
			rowIdx++
		}
		if x > 1 {
			cur.WriteString(strings.Repeat(" ", gap))
			x += gap
		}
		if item.actionKey != "" {
			hits = append(hits, hitRect{
				id: item.actionKey,
				x0: x,
				x1: x + need,
				y:  rowIdx,
			})
		}
		cur.WriteString(itemStr)
		x += need
	}
	if cur.Len() > 1 {
		rows = append(rows, cur.String())
	}
	if len(rows) == 0 {
		rows = []string{""}
	}
	return rows, hits
}

func smoothMeter(pct float64, width int) (string, string) {
	total := width * 8
	units := int(math.Round(float64(total) * pct / 100.0))
	if units < 0 {
		units = 0
	}
	if units > total {
		units = total
	}
	full := units / 8
	rem := units % 8
	var fill strings.Builder
	for i := 0; i < full; i++ {
		fill.WriteRune('█')
	}
	fillRunes := full
	if rem > 0 && full < width {
		fill.WriteRune(eighths[rem])
		fillRunes++
	}
	track := strings.Repeat("░", max(0, width-fillRunes))
	return fill.String(), track
}

func (m Model) levelStyle(pct float64, stale bool) lipgloss.Style {
	var s lipgloss.Style
	switch {
	case pct < 60:
		s = m.styles.green
	case pct < 85:
		s = m.styles.yellow
	default:
		s = m.styles.red
	}
	if stale {
		s = s.Faint(true)
	}
	return s
}

func (m Model) renderUsageSegments(u *provider.UsageInfo, bw int, withReset bool, labW int) string {
	if u == nil {
		return ""
	}
	stale := (u.Error != nil && *u.Error != "") || (u.AgeSeconds != nil && *u.AgeSeconds > 300)
	var parts []string
	now := m.now()

	for _, w := range u.Windows {
		fill, track := smoothMeter(w.Pct, bw)
		lab := fmt.Sprintf("%-*s ", labW, w.Label)
		pctText := fmt.Sprintf(" %3.0f%%", w.Pct)
		pctStyle := m.styles.text
		if stale {
			pctStyle = m.styles.dim
		}
		seg := m.styles.muted.Render(lab) +
			m.levelStyle(w.Pct, stale).Render(fill) +
			m.styles.track.Render(track) +
			pctStyle.Render(pctText)
		if withReset {
			if ep, ok := provider.ResetEpoch(w.ResetsAt); ok {
				cd := " ↻" + provider.FmtCountdown(ep, now)
				seg += m.styles.muted.Render(fmt.Sprintf("%-8s", cd))
			} else {
				seg += strings.Repeat(" ", 8)
			}
		}
		parts = append(parts, seg)
	}
	if u.Error != nil && *u.Error != "" {
		errText := *u.Error
		if strings.Contains(errText, "expired") || strings.Contains(errText, "offline") {
			parts = append(parts, m.styles.red.Render(errText))
		} else {
			parts = append(parts, m.styles.muted.Render(errText))
		}
	} else if stale && u.AgeSeconds != nil {
		parts = append(parts, m.styles.muted.Render(provider.FmtAge(*u.AgeSeconds)))
	}
	return strings.Join(parts, "  ")
}

func truncateRunes(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxW {
		return s
	}
	if maxW == 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

func padRightWidth(s string, target int) string {
	w := runewidth.StringWidth(s)
	if w >= target {
		return s
	}
	return s + strings.Repeat(" ", target-w)
}

func (m Model) computeLayout() layoutResult {
	footerLines, footerHits := m.renderFooter()
	w := m.width
	cw := max(30, min(w-2, 112))
	inner := cw - 2

	flat := m.rows()
	var realAccts []provider.AccountEntry
	for _, r := range flat {
		if r.account != nil {
			realAccts = append(realAccts, *r.account)
		}
	}

	nameW := 8
	aliasW := 0
	planW := 0
	labW := 2
	for _, a := range realAccts {
		if ew := runewidth.StringWidth(a.Email); ew > nameW {
			nameW = ew
		}
		if a.Alias != nil && *a.Alias != "" {
			if aw := runewidth.StringWidth(*a.Alias) + 2; aw > aliasW {
				aliasW = aw
			}
		}
		if a.Plan != nil && a.Plan.Label != "" {
			if pw := runewidth.StringWidth(a.Plan.Label) + 2; pw > planW {
				planW = pw
			}
		}
		if a.Usage != nil {
			for _, win := range a.Usage.Windows {
				if lw := runewidth.StringWidth(win.Label); lw > labW {
					labW = lw
				}
			}
		}
	}
	if nameW > 30 {
		nameW = 30
	}
	leftW := 9 + nameW
	if aliasW > 0 {
		leftW += 2 + aliasW
	}
	if planW > 0 {
		leftW += 2 + planW
	}

	var lines []string
	rowToLine := make([]int, len(flat))
	lineToRow := make(map[int]int)

	// Header line
	nAcc := len(realAccts)
	accWord := "accounts"
	if nAcc == 1 {
		accWord = "account"
	}
	leftHead := " " + m.styles.accent.Bold(true).Render("◆ ") +
		m.styles.bold.Render("Tori") +
		m.styles.muted.Render("  AI account switcher")
	if m.loading {
		leftHead += "  " + m.spinner.View()
	}
	summary := m.styles.muted.Render(fmt.Sprintf("%d tools · %d %s", len(m.providers), nAcc, accWord))
	gapW := cw - lipgloss.Width(leftHead) - lipgloss.Width(summary) + 1
	if gapW >= 2 {
		lines = append(lines, leftHead+strings.Repeat(" ", gapW)+summary)
	} else {
		lines = append(lines, leftHead)
	}
	lines = append(lines, "")

	boxed := func(content string) string {
		visW := lipgloss.Width(content)
		pad := inner - visW
		if pad < 0 {
			pad = 0
		}
		return " " + m.styles.border.Render("│") + content + strings.Repeat(" ", pad) + m.styles.border.Render("│")
	}

	idx := 0
	for _, p := range m.shownProviders() {
		accts := m.data[p.Key()]
		errText := m.errors[p.Key()]

		count := ""
		if len(accts) > 0 {
			count = fmt.Sprintf(" · %d", len(accts))
		}
		tag := " · usage off"
		if m.usageOn[p.Key()] {
			tag = " · usage on"
		}
		b := m.styles.brand(p.Key())
		titlePart := " " + b.iconStyle.Render(b.icon+" "+p.Title()) +
			m.styles.muted.Render(count) +
			m.styles.dim.Render(tag+" ")
		titleW := lipgloss.Width(titlePart)
		dashW := max(0, inner-1-titleW)
		top := " " + m.styles.border.Render("╭─") + titlePart + m.styles.border.Render(strings.Repeat("─", dashW)+"╮")
		lines = append(lines, top)

		if errText != "" || len(accts) == 0 {
			sel := idx == m.cur
			hov := idx == m.hoverRow && !sel
			rowToLine[idx] = len(lines)
			lineToRow[len(lines)] = idx
			mark := " "
			if sel {
				mark = m.styles.accent.Bold(true).Render("▌")
			} else if hov {
				mark = m.styles.accent.Render("›")
			}
			var msg string
			if errText != "" {
				msg = m.styles.red.Render(truncateRunes("error: "+errText, max(10, inner-4)))
			} else {
				st := m.styles.muted
				if sel || hov {
					st = m.styles.text
				}
				msg = st.Render(truncateRunes("no accounts yet — press a or click to add", max(10, inner-4)))
			}
			lines = append(lines, boxed(" "+mark+" "+msg))
			idx++
		} else {
			for _, a := range accts {
				sel := idx == m.cur
				hov := idx == m.hoverRow && !sel
				rowToLine[idx] = len(lines)
				lineToRow[len(lines)] = idx

				mark := " "
				if sel {
					mark = m.styles.accent.Bold(true).Render("▌")
				} else if hov {
					mark = m.styles.accent.Render("›")
				}
				dot := m.styles.track.Render("○")
				if a.Active {
					dot = m.styles.green.Render("●")
				}
				nameStyle := m.styles.muted
				if a.Active || sel || hov {
					nameStyle = m.styles.text
					if sel {
						nameStyle = m.styles.bold
					}
				}
				numStr := nameStyle.Render(fmt.Sprintf("%2d", a.Number))
				emailStr := nameStyle.Render(padRightWidth(truncateRunes(a.Email, nameW), nameW))

				var segs strings.Builder
				segs.WriteString(" ")
				segs.WriteString(mark)
				segs.WriteString(" ")
				segs.WriteString(dot)
				segs.WriteString(" ")
				segs.WriteString(numStr)
				segs.WriteString("  ")
				segs.WriteString(emailStr)

				if aliasW > 0 {
					segs.WriteString("  ")
					aliasTxt := ""
					if a.Alias != nil && *a.Alias != "" {
						aliasTxt = "[" + *a.Alias + "]"
					}
					segs.WriteString(m.styles.muted.Render(padRightWidth(aliasTxt, aliasW)))
				}
				if planW > 0 {
					segs.WriteString("  ")
					if a.Plan != nil && a.Plan.Label != "" {
						badge := m.styles.badge.Render(a.Plan.Label)
						bw := lipgloss.Width(badge)
						segs.WriteString(badge)
						if bw < planW {
							segs.WriteString(strings.Repeat(" ", planW-bw))
						}
					} else {
						segs.WriteString(strings.Repeat(" ", planW))
					}
				}

				var extraLine string
				if a.Note != "" {
					noteRendered := m.styles.muted.Render(a.Note)
					avail := inner - 1 - leftW - 3
					if lipgloss.Width(noteRendered) <= avail {
						segs.WriteString("   ")
						segs.WriteString(noteRendered)
					} else {
						extraLine = noteRendered
					}
				} else if a.Usage != nil {
					avail := inner - 1 - leftW - 3
					placed := false
					for _, cfg := range []struct {
						bw    int
						reset bool
					}{
						{10, true}, {8, true}, {6, true},
						{10, false}, {8, false}, {6, false}, {4, false},
					} {
						us := m.renderUsageSegments(a.Usage, cfg.bw, cfg.reset, labW)
						if lipgloss.Width(us) <= avail {
							segs.WriteString("   ")
							segs.WriteString(us)
							placed = true
							break
						}
					}
					if !placed {
						extraLine = m.renderUsageSegments(a.Usage, 8, true, labW)
					}
				}

				lines = append(lines, boxed(segs.String()))
				if extraLine != "" {
					lineToRow[len(lines)] = idx
					lines = append(lines, boxed(" "+mark+strings.Repeat(" ", 7)+extraLine))
				}
				idx++
			}
		}
		bot := " " + m.styles.border.Render("╰"+strings.Repeat("─", inner)+"╯")
		lines = append(lines, bot)
		lines = append(lines, "")
	}

	return layoutResult{
		lines:        lines,
		rowToLine:    rowToLine,
		lineToRow:    lineToRow,
		footerLines:  footerLines,
		footerHits:   footerHits,
		footerHeight: len(footerLines),
	}
}

type modalBtnSpec struct {
	id    string
	label string
	kind  string // "ok", "del", "no"
}

func (m Model) renderBtn(b modalBtnSpec) string {
	hov := b.id == m.hoverModalBtn
	switch b.kind {
	case "ok":
		if hov {
			return m.styles.btnOkHover.Render(b.label)
		}
		return m.styles.btnOk.Render(b.label)
	case "del":
		if hov {
			return m.styles.btnDelHover.Render(b.label)
		}
		return m.styles.btnDel.Render(b.label)
	default:
		if hov {
			return m.styles.btnNoHover.Render(b.label)
		}
		return m.styles.btnNo.Render(b.label)
	}
}

func (m Model) buildModal() (string, []hitRect, boxRect) {
	bw := max(32, min(m.width-4, 72))
	wrap := func(w int, s string) string {
		if w < 20 {
			w = 20
		}
		return lipgloss.NewStyle().Width(w).Render(s)
	}

	type relHit struct {
		id     string
		x0, x1 int
		line   int
	}
	var extraRelHits []relHit
	var parts []string
	var btnSpecs []modalBtnSpec
	boxW := min(bw, 66)

	switch m.modal {
	case modalAdd:
		p := m.modalProvider
		bodyW := boxW - 6

		// Build interactive provider tab strip (←/→ or click to switch tool).
		var tabStrs []string
		tx := 0
		for i, prov := range m.providers {
			b := m.styles.brand(prov.Key())
			tabID := "tab:" + prov.Key()
			label := b.icon + " " + prov.Title()
			var rendered string
			if prov.Key() == p.Key() {
				rendered = b.tabActive.Render(label)
			} else if m.hoverModalBtn == tabID {
				rendered = b.tabHover.Render(label)
			} else {
				rendered = b.tabIdle.Render(label)
			}
			tw := lipgloss.Width(rendered)
			if i > 0 {
				tx += 2
			}
			// Line 0 is title, line 1 is blank, line 2 is tab strip.
			extraRelHits = append(extraRelHits, relHit{id: tabID, x0: tx, x1: tx + tw, line: 2})
			tx += tw
			tabStrs = append(tabStrs, rendered)
		}
		title := m.styles.bold.Render("Add account") + "   " + m.styles.muted.Render("←/→ or click tool")
		tabBar := strings.Join(tabStrs, "  ")

		if m.addStep == addStepSave {
			var statusLine string
			if m.addStatus.ManagedSlot != nil {
				statusLine = m.styles.yellow.Render(fmt.Sprintf("● Updating slot %d: %s", *m.addStatus.ManagedSlot, m.addStatus.DisplayName))
			} else if p.Key() == "antigravity" {
				statusLine = m.styles.green.Render("● Live Antigravity login detected")
			} else {
				statusLine = m.styles.green.Render(fmt.Sprintf("● Ready to save: %s", m.addStatus.DisplayName))
			}
			parts = []string{title, "", tabBar, "", statusLine}
			if p.Key() == "antigravity" {
				parts = append(parts, m.styles.muted.Render(wrap(bodyW, "Give it an optional alias (Google email loads automatically when usage meters are on).")))
			}
			parts = append(parts, "", m.aliasInput.View(), "")
			btnSpecs = []modalBtnSpec{
				{id: "save", label: "Enter: Save", kind: "ok"},
				{id: "cancel", label: "Esc: Cancel", kind: "no"},
			}
		} else {
			parts = []string{title, "", tabBar, ""}
			if m.addStatus.ManagedSlot != nil {
				parts = append(parts, m.styles.green.Render(wrap(bodyW, fmt.Sprintf("● Already saved: %s (slot %d)", m.addStatus.DisplayName, *m.addStatus.ManagedSlot))))
			} else {
				parts = append(parts, m.styles.muted.Render("○ Not currently signed in."))
			}
			parts = append(parts, "")

			var step1 string
			switch p.Key() {
			case "claude":
				if m.addStatus.ManagedSlot != nil {
					step1 = "1. Log out and sign in to another account in Claude Code."
				} else {
					step1 = "1. Sign in to Claude Code (`claude` → /login)."
				}
			case "codex":
				if m.addStatus.ManagedSlot != nil {
					step1 = "1. Log out and sign in to another account in Codex (`codex login`)."
				} else {
					step1 = "1. Sign in to Codex (`codex login`)."
				}
			default:
				if m.addStatus.ManagedSlot != nil {
					step1 = "1. Log out and sign in to another account in T3 Code (Antigravity)."
				} else {
					step1 = "1. Sign in to Antigravity in T3 Code."
				}
			}
			parts = append(parts,
				m.styles.text.Render(wrap(bodyW, step1)),
				m.styles.text.Render(wrap(bodyW, "2. Press r (or click Re-check) — Tori will add it if it isn't already saved.")),
			)

			if m.addNotice != "" {
				noticeStyle := m.styles.yellow
				if m.addNoticeErr {
					noticeStyle = m.styles.red
				}
				parts = append(parts, "", noticeStyle.Render(wrap(bodyW, m.addNotice)))
			}

			btnSpecs = append(btnSpecs, modalBtnSpec{id: "recheck", label: "r / Enter: Re-check", kind: "ok"})
			if m.addStatus.ManagedSlot != nil {
				btnSpecs = append(btnSpecs, modalBtnSpec{id: "update", label: fmt.Sprintf("s: Update slot %d", *m.addStatus.ManagedSlot), kind: "no"})
			}
			btnSpecs = append(btnSpecs, modalBtnSpec{id: "cancel", label: "Esc: Cancel", kind: "no"})
			parts = append(parts, "")
		}

	case modalDelete:
		bodyW := boxW - 6
		b := m.styles.brand(m.modalProvider.Key())
		title := m.styles.red.Bold(true).Render(fmt.Sprintf("Remove %s %s slot %d?", b.icon, m.modalProvider.Title(), m.modalAccount.Number))
		who := m.styles.bold.Render(m.modalAccount.Email)
		if m.modalAccount.Alias != nil && *m.modalAccount.Alias != "" {
			who += " " + m.styles.muted.Render("["+*m.modalAccount.Alias+"]")
		}
		if m.modalAccount.Active {
			who += "  " + m.styles.yellow.Render("● active")
		}

		parts = []string{title, "", who, ""}
		if m.modalAccount.Active {
			other := m.nextOtherAccount(m.modalProvider.Key(), m.modalAccount.Number)
			if other != nil {
				parts = append(parts, m.styles.text.Render(wrap(bodyW, "This account is currently active. Removing only forgets Tori's saved copy — your live login stays signed in unless you switch.")))
				parts = append(parts, m.styles.muted.Render(wrap(bodyW, fmt.Sprintf("Tip: press s to switch to %s and remove this slot in one step.", other.Email))))
				btnSpecs = append(btnSpecs,
					modalBtnSpec{id: "switch_remove", label: "s: Switch & remove", kind: "ok"},
					modalBtnSpec{id: "remove", label: "y: Remove only", kind: "del"},
				)
			} else {
				parts = append(parts, m.styles.text.Render(wrap(bodyW, "This is your active login. Removing only forgets Tori's saved copy — your account is not deleted.")))
				parts = append(parts, m.styles.muted.Render(wrap(bodyW, "Press c to also clear the local login file on this machine.")))
				btnSpecs = append(btnSpecs,
					modalBtnSpec{id: "remove", label: "y: Remove only", kind: "del"},
					modalBtnSpec{id: "remove_clear", label: "c: Remove & clear login", kind: "no"},
				)
			}
		} else {
			parts = append(parts, m.styles.muted.Render(wrap(bodyW, "Only forgets Tori's saved copy — your account and current login are untouched. Remaining slots are renumbered.")))
			btnSpecs = append(btnSpecs, modalBtnSpec{id: "remove", label: "y: Remove", kind: "del"})
		}
		btnSpecs = append(btnSpecs, modalBtnSpec{id: "cancel", label: "n / Esc: Cancel", kind: "no"})
		parts = append(parts, "")

	case modalDisclaimer:
		boxW = min(bw, 64)
		bodyW := boxW - 6
		p := m.modalProvider
		b := m.styles.brand(p.Key())
		title := m.styles.bold.Render("Turn on usage meters for ") + b.iconStyle.Render(b.icon+" "+p.Title()) + m.styles.bold.Render("?")
		parts = []string{
			title,
			"",
			m.styles.text.Render(wrap(bodyW, config.DisclaimerHead)),
		}
		warnText, hasWarn := config.DisclaimerTool[p.Key()]
		if hasWarn {
			parts = append(parts, "", m.styles.yellow.Bold(true).Render(wrap(bodyW, "⚠ "+warnText)))
		}
		parts = append(parts, "", m.styles.muted.Render(wrap(bodyW, config.DisclaimerTail)), "")

		okLabel := "y / Enter: Turn On"
		if hasWarn {
			okLabel = "y / Enter: Accept & Turn On"
		}
		btnSpecs = []modalBtnSpec{
			{id: "accept", label: okLabel, kind: "ok"},
			{id: "cancel", label: "n / Esc: Cancel", kind: "no"},
		}
	default:
		return "", nil, boxRect{}
	}

	// Render buttons across 1 or 2 rows and record their relative hit coordinates.
	bodyW := boxW - 6
	renderedBtns := make([]string, len(btnSpecs))
	widths := make([]int, len(btnSpecs))
	totalW := 0
	for i, b := range btnSpecs {
		renderedBtns[i] = m.renderBtn(b)
		widths[i] = lipgloss.Width(renderedBtns[i])
		if i > 0 {
			totalW += 2
		}
		totalW += widths[i]
	}

	preContent := lipgloss.JoinVertical(lipgloss.Left, parts...)
	btnStartLine := len(strings.Split(preContent, "\n"))

	relHits := append([]relHit{}, extraRelHits...)
	var btnRows string
	if totalW > bodyW && len(btnSpecs) >= 3 {
		x := 0
		for i := 0; i < 2; i++ {
			if i > 0 {
				x += 2
			}
			relHits = append(relHits, relHit{id: btnSpecs[i].id, x0: x, x1: x + widths[i], line: btnStartLine})
			x += widths[i]
		}
		x = 0
		for i := 2; i < len(btnSpecs); i++ {
			if i > 2 {
				x += 2
			}
			relHits = append(relHits, relHit{id: btnSpecs[i].id, x0: x, x1: x + widths[i], line: btnStartLine + 2})
			x += widths[i]
		}
		btnRows = strings.Join(renderedBtns[:2], "  ") + "\n\n" + strings.Join(renderedBtns[2:], "  ")
	} else {
		x := 0
		for i := range btnSpecs {
			if i > 0 {
				x += 2
			}
			relHits = append(relHits, relHit{id: btnSpecs[i].id, x0: x, x1: x + widths[i], line: btnStartLine})
			x += widths[i]
		}
		btnRows = strings.Join(renderedBtns, "  ")
	}

	fullContent := preContent + "\n" + btnRows
	box := m.styles.modal.Width(boxW).Render(fullContent)

	renderedW := lipgloss.Width(box)
	renderedH := lipgloss.Height(box)
	placeH := max(1, m.height-2)
	leftX := max(0, (m.width-renderedW)/2)
	topY := 1 + max(0, (placeH-renderedH)/2) // 1 for the top header line

	// Modal box has 1-cell border + 1-line top padding + 2-col left padding.
	innerX := leftX + 3
	innerY := topY + 2
	hits := make([]hitRect, 0, len(relHits))
	for _, rh := range relHits {
		hits = append(hits, hitRect{
			id: rh.id,
			x0: innerX + rh.x0,
			x1: innerX + rh.x1,
			y:  innerY + rh.line,
		})
	}
	boxR := boxRect{
		x0: leftX,
		y0: topY,
		x1: leftX + renderedW,
		y1: topY + renderedH,
	}
	return box, hits, boxR
}

// View renders the TUI screen.
func (m Model) View() string {
	if m.width < 30 || m.height < 6 {
		return m.styles.red.Render("terminal too small")
	}

	if m.modal != modalNone {
		header := " " + m.styles.accent.Bold(true).Render("◆ ") +
			m.styles.bold.Render("Tori") +
			m.styles.muted.Render("  AI account switcher")
		modalBox, _, _ := m.buildModal()
		placed := lipgloss.Place(m.width, max(1, m.height-2), lipgloss.Center, lipgloss.Center, modalBox)
		return header + "\n" + placed
	}

	layout := m.computeLayout()
	bodyH := m.bodyHeight(layout.footerHeight)

	offset := m.offset
	if offset > max(0, len(layout.lines)-bodyH) {
		offset = max(0, len(layout.lines)-bodyH)
	}
	end := min(len(layout.lines), offset+bodyH)

	var out []string
	out = append(out, layout.lines[offset:end]...)
	for len(out) < bodyH {
		out = append(out, "")
	}

	// Status / scroll indicator line
	more := ""
	if offset > 0 {
		more += "▴"
	}
	if end < len(layout.lines) {
		more += "▾"
	}
	statusText := ""
	if m.statusMsg != "" {
		var st lipgloss.Style
		switch m.statusTone {
		case toneGreen:
			st = m.styles.green
		case toneYellow:
			st = m.styles.yellow
		case toneRed:
			st = m.styles.red
		default:
			st = m.styles.text
		}
		prefix := "  "
		if m.loading {
			prefix = "  " + m.spinner.View() + " "
		}
		avail := max(10, m.width-6)
		statusText = prefix + st.Render(truncateRunes(m.statusMsg, avail))
	}
	if more != "" {
		pad := max(1, m.width-lipgloss.Width(statusText)-len([]rune(more))-2)
		statusText += strings.Repeat(" ", pad) + m.styles.muted.Render(more)
	}
	out = append(out, statusText)
	out = append(out, layout.footerLines...)
	return strings.Join(out, "\n")
}

// Run starts the interactive Bubble Tea TUI.
func Run(env *provider.Env) int {
	m := NewModel(env)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithOutput(os.Stdout))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tori: %v\n", err)
		return 1
	}
	return 0
}
