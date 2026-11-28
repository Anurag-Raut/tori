package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
)

var (
	clockTickInterval = 30 * time.Second
	usageTickInterval = 60 * time.Second
	successMsgTTL     = 3 * time.Second
	errorMsgTTL       = 5 * time.Second
)

type rowID struct {
	providerKey string
	slotNum     int
	email       string
	placeholder bool
}

func idForRow(r flatRow) rowID {
	if r.account == nil {
		return rowID{providerKey: r.provider.Key(), placeholder: true}
	}
	return rowID{
		providerKey: r.provider.Key(),
		slotNum:     r.account.Number,
		email:       r.account.Email,
	}
}

type flatRow struct {
	provider provider.Provider
	account  *provider.AccountEntry // nil for empty or errored tool placeholder
	loadErr  bool
}

type lineRange struct {
	start int
	end   int // inclusive
}

type footerHit struct {
	action string
	x0, x1 int
}

type headerMsgKind int

const (
	msgNone headerMsgKind = iota
	msgSuccess
	msgInfo
	msgWarning
	msgError
)

type headerMsg struct {
	kind headerMsgKind
	text string
	seq  int
}

type usageResult struct {
	key      string
	accounts []provider.AccountEntry
	err      string
}

type usageLoadedMsg struct {
	results []usageResult
	forced  bool
	now     time.Time
}

type switchDoneMsg struct {
	ok          bool
	providerKey string
	email       string
	errText     string
	now         time.Time
}

type actionDoneMsg struct {
	ok        bool
	text      string
	refresh   bool
	selectKey string
	terminal  bool // the screen was released to an external command
	now       time.Time
}

type addStatusMsg struct {
	provider provider.Provider
	status   provider.StatusInfo
	plan     string
	recheck  bool
}

type clockTickMsg time.Time
type usageTickMsg time.Time
type clearHeaderMsg struct{ seq int }

// Model is the root Bubble Tea model for the redesigned Tori TUI.
type Model struct {
	env       *provider.Env
	providers []provider.Provider
	data      map[string][]provider.AccountEntry
	errors    map[string]string
	usageOn   map[string]bool

	selectedID rowID
	hoverID    *rowID

	width  int
	height int
	layout screenLayout
	now    time.Time

	viewport    viewport.Model
	rowRanges   []lineRange
	lineToRow   map[int]int
	hiddenBelow int
	footerHits  []footerHit

	loading   bool
	headerMsg headerMsg
	msgSeq    int

	dlg        dialogState
	aliasInput textinput.Model
	legacy     map[string][]string // old token files by provider key

	// execTerminal releases the screen to a command; tests replace it.
	execTerminal func(tea.ExecCommand, tea.ExecCallback) tea.Cmd

	spinner spinner.Model
	glyphs  glyphs
	theme   theme
	keys    keyMap
	noColor bool

	lastClickID rowID
	lastClickAt time.Time
}

// NewModel initializes the TUI synchronously from local disk/cache (zero network calls).
func NewModel(env *provider.Env) Model {
	if env == nil {
		env = provider.NewEnv()
	}
	tuiEnv := *env
	tuiEnv.Warn = func(string) {}

	ascii := detectASCII(&tuiEnv)
	noAnim := detectNoAnim(&tuiEnv)
	g := newGlyphs(ascii, noAnim)
	th := newTheme(ascii)
	km := newKeyMap(g)

	sp := spinner.New()
	if ascii {
		sp.Spinner = spinner.Spinner{
			Frames: []string{"-", "\\", "|", "/"},
			FPS:    time.Second / 8,
		}
	} else {
		sp.Spinner = spinner.MiniDot
	}
	sp.Style = th.brand

	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "optional"
	ti.CharLimit = 40
	ti.Width = 30
	ti.Cursor.Style = th.inputCursor
	ti.TextStyle = th.inputText
	ti.PlaceholderStyle = th.inputPlace

	now := time.Now()
	if tuiEnv.Now != nil {
		now = tuiEnv.Now()
	}

	lay := computeScreenLayout(100, 24)
	vp := viewport.New(lay.contentW, lay.bodyH)
	vp.MouseWheelEnabled = false

	m := Model{
		env:        &tuiEnv,
		providers:  provider.All(),
		data:       make(map[string][]provider.AccountEntry),
		errors:     make(map[string]string),
		usageOn:    make(map[string]bool),
		legacy:     make(map[string][]string),
		width:      100,
		height:     24,
		layout:     lay,
		now:        now,
		viewport:   vp,
		dlg:        dialogState{hoverIdx: -1},
		aliasInput: ti,
		spinner:    sp,
		glyphs:     g,
		theme:      th,
		keys:       km,
		noColor:    detectNoColor(&tuiEnv),
	}
	m.execTerminal = tea.Exec
	m.loadLocalData()
	if p := m.legacyProvider(); p != nil {
		m.setHeaderMsg(msgWarning, fmt.Sprintf("Old %s tokens on disk %s press x to delete", p.Title(), g.sep))
	}
	if m.anyUsageEnabled() && !m.env.DisableNetwork {
		m.loading = true
	}

	// Start selection on the first active account if any, otherwise the first row.
	rows := m.rows()
	if len(rows) > 0 {
		m.selectedID = idForRow(rows[0])
		for _, r := range rows {
			if r.account != nil && r.account.Active {
				m.selectedID = idForRow(r)
				break
			}
		}
	}
	m.syncViewport()
	return m
}

func (m *Model) currentNow() time.Time {
	if m.env != nil && m.env.Now != nil {
		return m.env.Now()
	}
	return time.Now()
}

func (m *Model) loadLocalData() {
	for _, p := range m.providers {
		m.usageOn[p.Key()] = m.env.IsUsageEnabled(p.Key())
		if sp, ok := p.(provider.SignInProvider); ok {
			m.legacy[p.Key()] = sp.LegacyTokenFiles(m.env)
		}
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
		if m.errors[p.Key()] != "" {
			out = append(out, flatRow{provider: p, account: nil, loadErr: true})
			continue
		}
		accts := m.data[p.Key()]
		if len(accts) == 0 {
			out = append(out, flatRow{provider: p, account: nil})
			continue
		}
		for i := range accts {
			a := accts[i]
			out = append(out, flatRow{provider: p, account: &a})
		}
	}
	return out
}

func (m Model) selectedIndex(rows []flatRow) int {
	if len(rows) == 0 {
		return -1
	}
	// 1. Exact (provider, email) match for accounts.
	if !m.selectedID.placeholder && m.selectedID.email != "" {
		for i, r := range rows {
			if r.provider.Key() == m.selectedID.providerKey && r.account != nil && r.account.Email == m.selectedID.email {
				return i
			}
		}
	}
	// 2. Exact (provider, slotNum) match for accounts.
	if !m.selectedID.placeholder && m.selectedID.slotNum > 0 {
		for i, r := range rows {
			if r.provider.Key() == m.selectedID.providerKey && r.account != nil && r.account.Number == m.selectedID.slotNum {
				return i
			}
		}
	}
	// 3. First row of the same provider.
	if m.selectedID.providerKey != "" {
		for i, r := range rows {
			if r.provider.Key() == m.selectedID.providerKey {
				return i
			}
		}
	}
	return 0
}

func (m Model) anyUsageEnabled() bool {
	for _, p := range m.providers {
		if m.usageOn[p.Key()] {
			return true
		}
	}
	return false
}

func (m Model) hasReadingRows() bool {
	if !m.loading {
		return false
	}
	for _, p := range m.providers {
		if !m.usageOn[p.Key()] {
			continue
		}
		for _, a := range m.data[p.Key()] {
			if rowState(a, true, true, m.now) == stateReading {
				return true
			}
		}
	}
	return false
}

func (m Model) hasOfflineRows() bool {
	for _, p := range m.providers {
		if !m.usageOn[p.Key()] {
			continue
		}
		for _, a := range m.data[p.Key()] {
			if rowState(a, true, m.loading, m.now) == stateOffline {
				return true
			}
		}
	}
	return false
}

// Init schedules background ticks and starts the initial async usage fetch when meters are on.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		clockTickCmd(),
		usageTickCmd(),
	}
	if m.anyUsageEnabled() {
		cmds = append(cmds, fetchUsageCmd(m.env, m.providers, false, ""))
		if !m.glyphs.noAnim {
			cmds = append(cmds, m.spinner.Tick)
		}
	}
	return tea.Batch(cmds...)
}

func clockTickCmd() tea.Cmd {
	return tea.Tick(clockTickInterval, func(t time.Time) tea.Msg {
		return clockTickMsg(t)
	})
}

func usageTickCmd() tea.Cmd {
	return tea.Tick(usageTickInterval, func(t time.Time) tea.Msg {
		return usageTickMsg(t)
	})
}

func clearHeaderCmd(seq int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return clearHeaderMsg{seq: seq}
	})
}

func (m *Model) setHeaderMsg(kind headerMsgKind, text string) tea.Cmd {
	m.msgSeq++
	m.headerMsg = headerMsg{
		kind: kind,
		text: text,
		seq:  m.msgSeq,
	}
	switch kind {
	case msgSuccess, msgInfo:
		return clearHeaderCmd(m.msgSeq, successMsgTTL)
	case msgError:
		return clearHeaderCmd(m.msgSeq, errorMsgTTL)
	default:
		return nil
	}
}

func fetchUsageCmd(env *provider.Env, providers []provider.Provider, force bool, onlyKey string) tea.Cmd {
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
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		if len(targets) == 0 {
			return usageLoadedMsg{forced: force, now: now}
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
		if env.Now != nil {
			now = env.Now()
		} else {
			now = time.Now()
		}
		return usageLoadedMsg{results: results, forced: force, now: now}
	}
}

func switchCmd(env *provider.Env, p provider.Provider, slotNum int, email string) tea.Cmd {
	return func() tea.Msg {
		e := *env
		e.Warn = func(string) {}
		_, err := provider.Switch(&e, p, fmt.Sprintf("%d", slotNum))
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		if err != nil {
			return switchDoneMsg{
				ok:          false,
				providerKey: p.Key(),
				email:       email,
				errText:     err.Error(),
				now:         now,
			}
		}
		return switchDoneMsg{
			ok:          true,
			providerKey: p.Key(),
			email:       email,
			now:         now,
		}
	}
}

func checkAddStatusCmd(env *provider.Env, p provider.Provider, recheck bool) tea.Cmd {
	return func() tea.Msg {
		st, _ := p.Status(env)
		return addStatusMsg{
			provider: p,
			status:   st,
			recheck:  recheck,
		}
	}
}

func addAccountCmd(env *provider.Env, p provider.Provider, alias string) tea.Cmd {
	return func() tea.Msg {
		var args []string
		if alias = strings.TrimSpace(alias); alias != "" {
			args = []string{alias}
		}
		out, err := p.Add(env, args)
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), now: now}
		}
		msg := fmt.Sprintf("Saved %s account", p.Title())
		if len(out) > 0 && out[0] != "" {
			msg = out[0]
		}
		return actionDoneMsg{ok: true, text: msg, refresh: true, selectKey: p.Key(), now: now}
	}
}

func removeAccountCmd(env *provider.Env, p provider.Provider, num int, email string) tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), now: now}
		}
		for _, s := range slots {
			if s.Number == num {
				if _, err := provider.Remove(env, p, s); err != nil {
					return actionDoneMsg{ok: false, text: err.Error(), now: now}
				}
				return actionDoneMsg{ok: true, text: fmt.Sprintf("Removed %s", email), refresh: true, now: now}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d no longer exists", num), now: now}
	}
}

func switchAndRemoveCmd(env *provider.Env, p provider.Provider, switchTo, removeNum int, email string) tea.Cmd {
	return func() tea.Msg {
		e := *env
		e.Warn = func(string) {}
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		if _, err := provider.Switch(&e, p, fmt.Sprintf("%d", switchTo)); err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), now: now}
		}
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), refresh: true, now: now}
		}
		for _, s := range slots {
			if s.Number == removeNum {
				if _, err := provider.Remove(env, p, s); err != nil {
					return actionDoneMsg{ok: false, text: err.Error(), refresh: true, now: now}
				}
				return actionDoneMsg{
					ok:      true,
					text:    fmt.Sprintf("Switched and removed %s", email),
					refresh: true,
					now:     now,
				}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d no longer exists", removeNum), refresh: true, now: now}
	}
}

func removeAndClearCmd(env *provider.Env, p provider.Provider, num int, email string) tea.Cmd {
	return func() tea.Msg {
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		slots, err := p.Slots(env)
		if err != nil {
			return actionDoneMsg{ok: false, text: err.Error(), now: now}
		}
		for _, s := range slots {
			if s.Number == num {
				if _, err := provider.Remove(env, p, s); err != nil {
					return actionDoneMsg{ok: false, text: err.Error(), now: now}
				}
				if _, err := p.ClearLive(env); err != nil {
					return actionDoneMsg{ok: false, text: fmt.Sprintf("Removed %s, couldn't clear login", email), refresh: true, now: now}
				}
				return actionDoneMsg{
					ok:      true,
					text:    fmt.Sprintf("Removed %s and cleared login", email),
					refresh: true,
					now:     now,
				}
			}
		}
		return actionDoneMsg{ok: false, text: fmt.Sprintf("Slot %d no longer exists", num), now: now}
	}
}

func rotateAllCmd(env *provider.Env, providers []provider.Provider) tea.Cmd {
	return func() tea.Msg {
		e := *env
		e.Warn = func(string) {}
		now := time.Now()
		if env.Now != nil {
			now = env.Now()
		}
		rotated := 0
		skipped := ""
		var failed []string
		for _, p := range providers {
			slots, _ := p.Slots(&e)
			if len(slots) < 2 {
				continue
			}
			if _, ok := p.(provider.SignInProvider); ok {
				// Switching here is a sign-in the user asks for by account.
				skipped = p.Title()
				continue
			}
			rotated++
			if _, err := provider.Switch(&e, p, ""); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %s", p.Title(), err.Error()))
			}
		}
		if rotated == 0 && skipped != "" {
			return actionDoneMsg{ok: false, text: skipped + " needs a sign-in: select an account", now: now}
		}
		if rotated == 0 {
			return actionDoneMsg{ok: false, text: "Need at least 2 accounts to rotate", now: now}
		}
		if len(failed) > 0 {
			return actionDoneMsg{ok: false, text: strings.Join(failed, "; "), refresh: true, now: now}
		}
		text := "Rotated all tools"
		if skipped != "" {
			text = "Rotated, except " + skipped + " (needs sign-in)"
		}
		return actionDoneMsg{ok: true, text: text, refresh: true, now: now}
	}
}

// Update handles Bubble Tea messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout = computeScreenLayout(m.width, m.height)
		m.viewport.Width = max(1, m.layout.contentW)
		m.viewport.Height = max(1, m.layout.bodyH)
		m.syncViewport()
		return m, nil

	case clockTickMsg:
		m.now = m.currentNow()
		m.syncViewport()
		return m, clockTickCmd()

	case usageTickMsg:
		m.now = m.currentNow()
		if !m.loading && m.anyUsageEnabled() {
			m.loading = true
			cmds := []tea.Cmd{
				usageTickCmd(),
				fetchUsageCmd(m.env, m.providers, false, ""),
			}
			if !m.glyphs.noAnim && m.hasReadingRows() {
				cmds = append(cmds, m.spinner.Tick)
			}
			m.syncViewport()
			return m, tea.Batch(cmds...)
		}
		return m, usageTickCmd()

	case clearHeaderMsg:
		if m.headerMsg.seq == msg.seq {
			m.headerMsg = headerMsg{}
		}
		return m, nil

	case spinner.TickMsg:
		if !m.loading || m.glyphs.noAnim {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.hasReadingRows() {
			m.syncViewport()
		}
		return m, cmd

	case usageLoadedMsg:
		m.loading = false
		if !msg.now.IsZero() {
			m.now = msg.now
		}
		for _, res := range msg.results {
			if res.err != "" {
				m.errors[res.key] = res.err
				m.data[res.key] = nil
			} else {
				m.errors[res.key] = ""
				m.data[res.key] = res.accounts
			}
		}
		m.reconcileSelection()
		m.syncViewport()
		return m, nil

	case switchDoneMsg:
		if !msg.now.IsZero() {
			m.now = msg.now
		}
		m.loadLocalData()
		m.reconcileSelection()
		m.syncViewport()
		if msg.ok {
			return m, m.setHeaderMsg(msgSuccess, fmt.Sprintf("Switched to %s", msg.email))
		}
		return m, m.setHeaderMsg(msgError, fmt.Sprintf("couldn't switch %s %s", m.glyphs.sep, msg.errText))

	case actionDoneMsg:
		if !msg.now.IsZero() {
			m.now = msg.now
		}
		if msg.refresh {
			m.loadLocalData()
			m.reconcileSelection()
			m.syncViewport()
		}
		// Releasing the screen turns mouse reporting off; turn it back on.
		var mouse tea.Cmd
		if msg.terminal {
			mouse = tea.EnableMouseAllMotion
		}
		if msg.ok {
			cmds := []tea.Cmd{mouse}
			if c := m.setHeaderMsg(msgSuccess, msg.text); c != nil {
				cmds = append(cmds, c)
			}
			if msg.selectKey != "" && m.usageOn[msg.selectKey] {
				m.loading = true
				cmds = append(cmds, fetchUsageCmd(m.env, m.providers, false, msg.selectKey))
				if !m.glyphs.noAnim {
					cmds = append(cmds, m.spinner.Tick)
				}
			}
			return m, tea.Batch(cmds...)
		}
		return m, tea.Batch(mouse, m.setHeaderMsg(msgError, msg.text))

	case addStatusMsg:
		return m.handleAddStatus(msg)

	case installDoneMsg:
		return m.handleInstallDone(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		if key.Matches(msg, m.keys.ForceQuit) {
			return m, tea.Quit
		}
		if m.dlg.kind != dialogNone {
			return m.handleDialogKey(msg)
		}
		return m.handleMainKey(msg)
	}
	return m, nil
}

func (m *Model) reconcileSelection() {
	rows := m.rows()
	idx := m.selectedIndex(rows)
	if idx >= 0 && idx < len(rows) {
		m.selectedID = idForRow(rows[idx])
	}
}

func (m Model) handleAddStatus(msg addStatusMsg) (Model, tea.Cmd) {
	st := msg.status
	m.dlg.provider = msg.provider
	m.aliasInput.Placeholder = "optional"
	m.dlg.status = st
	m.dlg.cursor = 0
	m.dlg.hoverIdx = -1

	// Case 1: Signed in to an unsaved account -> go directly to Step 4 (Save).
	if st.LoggedIn && st.ManagedSlot == nil {
		m.dlg.kind = dialogAddSave
		m.dlg.stillSameLogin = false
		m.dlg.notFound = false
		m.dlg.account = nil
		m.aliasInput.Reset()
		return m, m.aliasInput.Focus()
	}

	// Case 2: Signed in to an already-saved slot.
	if st.LoggedIn && st.ManagedSlot != nil {
		m.dlg.notFound = false
		slotN := *st.ManagedSlot
		if msg.recheck {
			// If still signed in to the exact same saved slot we started with,
			// stay on Step 2 and show the warning.
			if m.dlg.initialSlot != nil && *m.dlg.initialSlot == slotN {
				m.dlg.kind = dialogAddSignIn
				m.dlg.stillSameLogin = true
				return m, nil
			}
			// Signed in to a different saved slot -> Step 3 (Already saved).
			m.dlg.kind = dialogAddAlreadySaved
			m.dlg.stillSameLogin = false
			return m, nil
		}

		// Initial open from Step 1: if the currently selected row is this slot
		// AND its login was expired, offer Step 3 (Update slot) immediately.
		for _, a := range m.data[msg.provider.Key()] {
			if a.Number == slotN && rowState(a, m.usageOn[msg.provider.Key()], m.loading, m.now) == stateLoginExpired {
				m.dlg.kind = dialogAddAlreadySaved
				m.dlg.stillSameLogin = false
				return m, nil
			}
		}

		// Otherwise show Step 2 (Sign in with a new account) and remember this slot.
		cp := slotN
		m.dlg.initialSlot = &cp
		m.dlg.kind = dialogAddSignIn
		m.dlg.stillSameLogin = false
		return m, nil
	}

	// Case 3: Not signed in yet -> Step 2 (Sign in).
	m.dlg.kind = dialogAddSignIn
	m.dlg.stillSameLogin = false
	m.dlg.notFound = msg.recheck
	if !msg.recheck {
		m.dlg.initialSlot = nil
	}
	return m, nil
}

func (m Model) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.rows()
	n := len(rows)
	cur := m.selectedIndex(rows)

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.dlg.close()
		m.dlg.kind = dialogHelp
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if n > 0 && cur < n-1 {
			m.selectedID = idForRow(rows[cur+1])
			m.syncViewport()
		}
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if n > 0 && cur > 0 {
			m.selectedID = idForRow(rows[cur-1])
			m.syncViewport()
		}
		return m, nil

	case key.Matches(msg, m.keys.NextTool):
		if n > 0 && cur >= 0 {
			shown := m.shownProviders()
			curKey := rows[cur].provider.Key()
			curToolIdx := 0
			for i, p := range shown {
				if p.Key() == curKey {
					curToolIdx = i
					break
				}
			}
			nextKey := shown[(curToolIdx+1)%len(shown)].Key()
			for _, r := range rows {
				if r.provider.Key() == nextKey {
					m.selectedID = idForRow(r)
					break
				}
			}
			m.syncViewport()
		}
		return m, nil

	case key.Matches(msg, m.keys.Switch):
		if n == 0 || cur < 0 {
			return m, nil
		}
		return m.activateRow(rows[cur])

	case key.Matches(msg, m.keys.Add):
		return m.openAddPickTool(rows, cur)

	case key.Matches(msg, m.keys.Remove):
		if n == 0 || cur < 0 || rows[cur].account == nil {
			return m, nil
		}
		return m.openRemoveDialog(rows[cur].provider, *rows[cur].account), nil

	case key.Matches(msg, m.keys.Meters):
		if n == 0 || cur < 0 {
			return m, nil
		}
		p := rows[cur].provider
		if m.usageOn[p.Key()] {
			if err := config.SetUsage(m.env.Root, []string{p.Key()}, false); err != nil {
				return m, m.setHeaderMsg(msgError, err.Error())
			}
			m.usageOn[p.Key()] = false
			m.loadLocalData()
			m.reconcileSelection()
			m.syncViewport()
			return m, m.setHeaderMsg(msgSuccess, fmt.Sprintf("Meters off for %s", p.Title()))
		}
		m.dlg.close()
		m.dlg.kind = dialogMetersOn
		m.dlg.provider = p
		m.dlg.cursor = 0 // safe default: Turn on for standard, Not now for Antigravity
		return m, nil

	case key.Matches(msg, m.keys.Rotate):
		return m, rotateAllCmd(m.env, m.providers)

	case key.Matches(msg, m.keys.Cleanup):
		return m.openCleanup(), nil

	case key.Matches(msg, m.keys.Refresh):
		// Reload any errored tools locally first.
		m.loadLocalData()
		m.reconcileSelection()
		if !m.anyUsageEnabled() {
			m.syncViewport()
			return m, m.setHeaderMsg(msgInfo, fmt.Sprintf("Meters off %s press u to turn on", m.glyphs.sep))
		}
		if m.loading {
			return m, nil
		}
		m.loading = true
		m.syncViewport()
		cmds := []tea.Cmd{fetchUsageCmd(m.env, m.providers, true, "")}
		if !m.glyphs.noAnim && m.hasReadingRows() {
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m Model) activateRow(row flatRow) (Model, tea.Cmd) {
	if row.loadErr {
		m.loadLocalData()
		m.reconcileSelection()
		m.syncViewport()
		return m, nil
	}
	sp, signsIn := row.provider.(provider.SignInProvider)
	if row.account != nil && signsIn && !sp.Installed(m.env) {
		return m.openInstall(sp, func(m Model) (Model, tea.Cmd) { return m.activateRow(row) }), nil
	}
	if row.account == nil {
		if signsIn {
			return m.openSignIn(sp)
		}
		// Empty tool -> jump directly to checking status for this tool.
		m.dlg.close()
		m.dlg.provider = row.provider
		return m, checkAddStatusCmd(m.env, row.provider, false)
	}
	if row.account.Active {
		if signsIn {
			return m.openAccount(sp, *row.account), nil
		}
		return m, m.setHeaderMsg(msgInfo, "Already active")
	}
	if signsIn {
		// No optimistic update: the switch happens only if the sign-in succeeds.
		num := row.account.Number
		email := row.account.Email
		return m.withTargets(sp, "Switch Antigravity to "+email, func(m Model, ids []string) (Model, tea.Cmd) {
			return m, m.signInCmd(sp, fmt.Sprintf("Switched to %s", email), func(env *provider.Env) error {
				env.Targets = ids
				_, err := provider.Switch(env, sp, fmt.Sprintf("%d", num))
				return err
			})
		})
	}

	// Optimistic update: move active dot immediately; switchDoneMsg reconciles or rolls back.
	accts := m.data[row.provider.Key()]
	updated := make([]provider.AccountEntry, len(accts))
	for i, a := range accts {
		a.Active = a.Number == row.account.Number
		updated[i] = a
	}
	m.data[row.provider.Key()] = updated
	m.syncViewport()

	return m, switchCmd(m.env, row.provider, row.account.Number, row.account.Email)
}

func (m Model) openAddPickTool(rows []flatRow, cur int) (Model, tea.Cmd) {
	m.dlg.close()
	m.dlg.kind = dialogAddPickTool
	// Default cursor to currently selected tool.
	if cur >= 0 && cur < len(rows) {
		curKey := rows[cur].provider.Key()
		for i, p := range m.providers {
			if p.Key() == curKey {
				m.dlg.cursor = i
				break
			}
		}
	}
	return m, nil
}

func (m Model) openRemoveDialog(p provider.Provider, acct provider.AccountEntry) Model {
	m.dlg.close()
	acctCopy := acct
	m.dlg.kind = dialogRemove
	m.dlg.provider = p
	m.dlg.account = &acctCopy

	accts := m.data[p.Key()]
	m.dlg.onlyAccount = len(accts) <= 1
	if m.dlg.removingInUse() && len(accts) > 1 {
		m.dlg.otherSlot = nextOtherSlot(accts, acct.Number)
	}

	// Safe default selection: never a destructive choice.
	switch {
	case !m.dlg.removingInUse():
		m.dlg.cursor = 0 // Cancel
	case !m.dlg.onlyAccount && m.dlg.otherSlot > 0:
		m.dlg.cursor = 0 // Switch to slot N, then remove
	default:
		m.dlg.cursor = 2 // Cancel (choices are Remove only, Remove and clear login, Cancel)
	}
	return m
}

func nextOtherSlot(accts []provider.AccountEntry, exclude int) int {
	for _, a := range accts {
		if a.Number > exclude {
			return a.Number
		}
	}
	for _, a := range accts {
		if a.Number != exclude {
			return a.Number
		}
	}
	return 0
}

func (m Model) handleDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Back) {
		m.aliasInput.Blur()
		m.dlg.close()
		return m, nil
	}

	switch m.dlg.kind {
	case dialogHelp:
		if key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Switch) || key.Matches(msg, m.keys.Quit) {
			m.dlg.close()
		}
		return m, nil

	case dialogAddName:
		return m.handleAddNameKey(msg)

	case dialogTargets:
		return m.handleTargetsKey(msg)

	case dialogAddSignIn:
		if key.Matches(msg, m.keys.Switch) {
			return m, checkAddStatusCmd(m.env, m.dlg.provider, true)
		}
		return m, nil

	case dialogAddSave:
		if key.Matches(msg, m.keys.Switch) {
			p := m.dlg.provider
			alias := m.aliasInput.Value()
			m.aliasInput.Blur()
			m.dlg.close()
			return m, addAccountCmd(m.env, p, alias)
		}
		var cmd tea.Cmd
		m.aliasInput, cmd = m.aliasInput.Update(msg)
		return m, cmd

	default:
		choices := m.dialogChoices()
		if len(choices) == 0 {
			return m, nil
		}
		switch {
		case key.Matches(msg, m.keys.Down):
			m.dlg.cursor = (m.dlg.cursor + 1) % len(choices)
			return m, nil
		case key.Matches(msg, m.keys.Up):
			m.dlg.cursor = (m.dlg.cursor - 1 + len(choices)) % len(choices)
			return m, nil
		case key.Matches(msg, m.keys.Switch):
			if m.dlg.cursor >= 0 && m.dlg.cursor < len(choices) {
				return m.executeDialogChoice(choices[m.dlg.cursor].id)
			}
		}
	}
	return m, nil
}

func (m Model) executeDialogChoice(id string) (tea.Model, tea.Cmd) {
	if id == "cancel" {
		m.aliasInput.Blur()
		m.dlg.close()
		return m, nil
	}

	switch m.dlg.kind {
	case dialogTargets:
		if tid, ok := strings.CutPrefix(id, "target:"); ok {
			for i, t := range m.dlg.targets {
				if t.ID == tid {
					m.dlg.cursor = i
					return m.toggleTarget(), nil
				}
			}
		}
		return m, nil

	case dialogAddName:
		return m.submitName(id)

	case dialogInstall:
		if p, ok := m.dlg.provider.(provider.SignInProvider); ok && id == "install" {
			then := m.dlg.then
			m.dlg.close()
			return m, m.installCmd(p, then)
		}

	case dialogAccount:
		p, ok := m.dlg.provider.(provider.SignInProvider)
		if !ok || m.dlg.account == nil {
			m.dlg.close()
			return m, nil
		}
		acct := *m.dlg.account
		m.dlg.close()
		switch id {
		case "signin":
			return m.withTargets(p, "Sign in Antigravity as "+acct.Email, func(m Model, ids []string) (Model, tea.Cmd) {
				return m, m.signInCmd(p, fmt.Sprintf("Signed in to %s", acct.Email), func(env *provider.Env) error {
					env.Targets = ids
					_, err := p.Login(env, fmt.Sprintf("%d", acct.Number))
					return err
				})
			})
		case "signout":
			return m.withTargets(p, "Sign out of Antigravity", func(m Model, ids []string) (Model, tea.Cmd) {
				return m, m.signInCmd(p, "Signed out. No account is marked signed in there", func(env *provider.Env) error {
					env.Targets = ids
					_, err := p.Logout(env)
					return err
				})
			})
		}

	case dialogCleanup:
		if p, ok := m.dlg.provider.(provider.SignInProvider); ok && id == "delete" {
			m.dlg.close()
			return m, deleteLegacyCmd(m.env, p)
		}

	case dialogAddPickTool:
		if keyName, ok := strings.CutPrefix(id, "tool:"); ok {
			for _, p := range m.providers {
				if p.Key() == keyName {
					if sp, ok := p.(provider.SignInProvider); ok {
						return m.openSignIn(sp)
					}
					m.dlg.provider = p
					return m, checkAddStatusCmd(m.env, p, false)
				}
			}
		}

	case dialogAddAlreadySaved:
		if id == "update" {
			p := m.dlg.provider
			m.dlg.close()
			return m, addAccountCmd(m.env, p, "")
		}

	case dialogRemove:
		if m.dlg.account == nil || m.dlg.provider == nil {
			m.dlg.close()
			return m, nil
		}
		p := m.dlg.provider
		acct := *m.dlg.account
		other := m.dlg.otherSlot
		m.dlg.close()
		switch id {
		case "remove":
			return m, removeAccountCmd(m.env, p, acct.Number, acct.Email)
		case "switch_remove":
			return m, switchAndRemoveCmd(m.env, p, other, acct.Number, acct.Email)
		case "remove_clear":
			return m, removeAndClearCmd(m.env, p, acct.Number, acct.Email)
		}

	case dialogMetersOn:
		if id == "enable" && m.dlg.provider != nil {
			p := m.dlg.provider
			m.dlg.close()
			if err := config.SetUsage(m.env.Root, []string{p.Key()}, true); err != nil {
				return m, m.setHeaderMsg(msgError, err.Error())
			}
			m.usageOn[p.Key()] = true
			m.loadLocalData()
			m.loading = true
			m.reconcileSelection()
			m.syncViewport()
			cmds := []tea.Cmd{
				fetchUsageCmd(m.env, m.providers, false, p.Key()),
				m.setHeaderMsg(msgSuccess, fmt.Sprintf("Meters on for %s", p.Title())),
			}
			if !m.glyphs.noAnim {
				cmds = append(cmds, m.spinner.Tick)
			}
			return m, tea.Batch(cmds...)
		}
	}
	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.layout.mode == layoutTooSmall {
		return m, nil
	}

	if m.dlg.kind != dialogNone {
		_, hits, rect := m.buildDialog()
		switch {
		case msg.Action == tea.MouseActionMotion:
			m.dlg.hoverIdx = -1
			for i, h := range hits {
				if msg.Y == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					m.dlg.hoverIdx = i
					break
				}
			}
			return m, nil

		case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
			for i, h := range hits {
				if msg.Y == h.y && msg.X >= h.x0 && msg.X < h.x1 {
					m.dlg.cursor = i
					return m.executeDialogChoice(h.id)
				}
			}
			if msg.X < rect.x0 || msg.X >= rect.x1 || msg.Y < rect.y0 || msg.Y >= rect.y1 {
				m.aliasInput.Blur()
				m.dlg.close()
			}
		}
		return m, nil
	}

	rows := m.rows()
	if len(rows) == 0 {
		return m, nil
	}
	bodyTop := 2
	bodyEnd := bodyTop + m.layout.bodyH
	footerY := bodyEnd

	switch {
	case msg.Action == tea.MouseActionMotion:
		var nextHover *rowID
		if msg.Y >= bodyTop && msg.Y < bodyEnd {
			listLine := (msg.Y - bodyTop) + m.viewport.YOffset
			if idx, ok := m.lineToRow[listLine]; ok && idx >= 0 && idx < len(rows) {
				id := idForRow(rows[idx])
				nextHover = &id
			}
		}
		if !sameHover(m.hoverID, nextHover) {
			m.hoverID = nextHover
			m.syncViewport()
		}
		return m, nil

	case msg.Button == tea.MouseButtonWheelUp && msg.Action == tea.MouseActionPress:
		cur := m.selectedIndex(rows)
		if cur > 0 {
			m.selectedID = idForRow(rows[cur-1])
			m.syncViewport()
		}
		return m, nil

	case msg.Button == tea.MouseButtonWheelDown && msg.Action == tea.MouseActionPress:
		cur := m.selectedIndex(rows)
		if cur >= 0 && cur < len(rows)-1 {
			m.selectedID = idForRow(rows[cur+1])
			m.syncViewport()
		}
		return m, nil

	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		if msg.Y == footerY {
			for _, fh := range m.footerHits {
				if msg.X >= fh.x0 && msg.X < fh.x1 && fh.action != "" {
					return m.handleMainKey(keyMsgFromAction(fh.action))
				}
			}
			return m, nil
		}
		if msg.Y < bodyTop || msg.Y >= bodyEnd {
			return m, nil
		}
		listLine := (msg.Y - bodyTop) + m.viewport.YOffset
		idx, ok := m.lineToRow[listLine]
		if !ok || idx < 0 || idx >= len(rows) {
			return m, nil
		}
		clickedID := idForRow(rows[idx])
		now := m.currentNow()
		isDouble := m.lastClickID == clickedID && now.Sub(m.lastClickAt) < 450*time.Millisecond
		m.selectedID = clickedID
		m.lastClickID = clickedID
		m.lastClickAt = now
		m.syncViewport()

		if isDouble {
			return m.activateRow(rows[idx])
		}
	}
	return m, nil
}

func sameHover(a, b *rowID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func keyMsgFromAction(action string) tea.KeyMsg {
	switch action {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(action)}
	}
}

// syncViewport rebuilds the grouped list content, updates row line mappings,
// keeps the selected row (including expanded lines) fully visible, and counts hidden rows below.
func (m *Model) syncViewport() {
	if m.layout.mode == layoutTooSmall {
		return
	}
	rows := m.rows()
	selIdx := m.selectedIndex(rows)

	var lines []string
	rowRanges := make([]lineRange, len(rows))
	lineToRow := make(map[int]int)

	rowCursor := 0
	shown := m.shownProviders()

	for groupIdx, p := range shown {
		if groupIdx > 0 {
			lines = append(lines, "")
		}
		metersOn := m.usageOn[p.Key()]
		hasErr := m.errors[p.Key()] != ""
		accts := m.data[p.Key()]

		lines = append(lines, renderToolHeader(p, metersOn, m.layout, m.theme))

		if hasErr {
			selected := rowCursor == selIdx
			hovered := !selected && m.hoverID != nil && *m.hoverID == idForRow(rows[rowCursor])
			rs := m.rowStylesFor(selected, hovered)
			start := len(lines)
			lines = append(lines, renderErrorToolRow(rs, m.layout.contentW, m.glyphs))
			rowRanges[rowCursor] = lineRange{start: start, end: start}
			lineToRow[start] = rowCursor
			rowCursor++
			continue
		}

		if len(accts) == 0 {
			selected := rowCursor == selIdx
			hovered := !selected && m.hoverID != nil && *m.hoverID == idForRow(rows[rowCursor])
			rs := m.rowStylesFor(selected, hovered)
			start := len(lines)
			lines = append(lines, renderEmptyToolRow(rs, m.layout.contentW, m.glyphs))
			rowRanges[rowCursor] = lineRange{start: start, end: start}
			lineToRow[start] = rowCursor
			rowCursor++
			continue
		}

		maxWins := 0
		for _, a := range accts {
			if a.Usage != nil && len(a.Usage.Windows) > maxWins {
				maxWins = len(a.Usage.Windows)
			}
		}
		cols := computeWideColumns(m.layout.contentW, maxWins)

		for _, a := range accts {
			selected := rowCursor == selIdx
			hovered := !selected && m.hoverID != nil && *m.hoverID == idForRow(rows[rowCursor])
			rs := m.rowStylesFor(selected, hovered)
			state := rowState(a, metersOn, m.loading, m.now)

			var block string
			if m.layout.mode == layoutWide {
				block = renderWideAccountRow(a, state, cols, rs, m.glyphs, m.noColor, m.spinner.View(), m.now)
			} else {
				block = renderNarrowAccountRow(a, state, selected, m.layout.contentW, rs, m.glyphs, m.noColor, m.spinner.View(), m.now)
			}

			blockLines := strings.Split(block, "\n")
			start := len(lines)
			for _, bl := range blockLines {
				lineToRow[len(lines)] = rowCursor
				lines = append(lines, bl)
			}
			rowRanges[rowCursor] = lineRange{start: start, end: len(lines) - 1}
			rowCursor++
		}
	}

	m.rowRanges = rowRanges
	m.lineToRow = lineToRow
	m.viewport.Width = m.layout.contentW
	m.viewport.Height = m.layout.bodyH
	m.viewport.SetContent(strings.Join(lines, "\n"))

	// Ensure selected row (including all expanded lines) is fully visible.
	maxOff := max(0, len(lines)-m.layout.bodyH)
	off := m.viewport.YOffset
	if off > maxOff {
		off = maxOff
	}
	if selIdx >= 0 && selIdx < len(rowRanges) {
		rng := rowRanges[selIdx]
		if rng.start < off {
			off = rng.start
			// Include the tool header directly above when jumping to the top of a group.
			if off > 0 {
				if _, isRow := lineToRow[off-1]; !isRow && lines[off-1] != "" {
					off--
				}
			}
		}
		if rng.end >= off+m.layout.bodyH {
			off = rng.end - m.layout.bodyH + 1
			if off > rng.start {
				off = rng.start
			}
		}
	}
	if off < 0 {
		off = 0
	}
	if off > maxOff {
		off = maxOff
	}
	m.viewport.SetYOffset(off)

	// Count rows hidden below the viewport for the header scroll indicator.
	bottom := off + m.layout.bodyH
	hidden := 0
	for _, rng := range rowRanges {
		if rng.start >= bottom {
			hidden++
		}
	}
	if hidden == 0 && bottom < len(lines) {
		hidden = 1
	}
	m.hiddenBelow = hidden

	// Rebuild footer hits to match current layout mode.
	_, m.footerHits = m.renderFooter()
}

func (m Model) rowStylesFor(selected, hovered bool) rowStyles {
	if selected {
		return m.theme.rowSelected
	}
	if hovered {
		return m.theme.rowHover
	}
	return m.theme.rowNormal
}

func (m Model) renderHeader() string {
	left := " " + m.theme.brand.Render("Tori")

	var right string
	switch {
	case m.headerMsg.kind == msgSuccess:
		right = m.theme.good.Render(m.glyphs.done + " " + m.headerMsg.text)
	case m.headerMsg.kind == msgError:
		right = m.theme.bad.Render(m.glyphs.bad + " " + m.headerMsg.text)
	case m.headerMsg.kind == msgWarning:
		right = m.theme.warn.Render(m.glyphs.warn + " " + m.headerMsg.text)
	case m.headerMsg.kind == msgInfo:
		right = m.theme.dim.Render(m.headerMsg.text)
	case m.hasOfflineRows():
		right = m.theme.warn.Render(fmt.Sprintf("%s offline %s showing last readings", m.glyphs.warn, m.glyphs.sep))
	case m.hiddenBelow > 0:
		right = m.theme.dim.Render(fmt.Sprintf("%s %d more", m.glyphs.down, m.hiddenBelow))
	}

	if right == "" {
		return left
	}
	maxRight := max(0, m.layout.contentW-1-lipgloss.Width(left)-2)
	if maxRight == 0 {
		return left
	}
	if lipgloss.Width(right) > maxRight {
		right = fitCell(right, maxRight, false)
	}
	gap := m.layout.contentW - 1 - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func (m Model) renderFooter() (string, []footerHit) {
	hints := wideFooterHints(m.glyphs)
	if m.layout.mode == layoutNarrow {
		hints = narrowFooterHints(m.glyphs)
	}
	hints = m.adjustFooterHints(hints)
	var parts []string
	var hits []footerHit
	x := 1
	for i, h := range hints {
		if i > 0 {
			parts = append(parts, "  ")
			x += 2
		}
		item := m.theme.key.Render(h.keyLabel) + " " + m.theme.dim.Render(h.desc)
		w := lipgloss.Width(item)
		if x+w > m.layout.contentW {
			break
		}
		parts = append(parts, item)
		if h.action != "" {
			hits = append(hits, footerHit{action: h.action, x0: x, x1: x + w})
		}
		x += w
	}
	return " " + strings.Join(parts, ""), hits
}

// adjustFooterHints fits the hints to the selected row and to old tokens.
func (m Model) adjustFooterHints(hints []footerHint) []footerHint {
	rows := m.rows()
	if i := m.selectedIndex(rows); i >= 0 {
		row := rows[i]
		if _, signsIn := row.provider.(provider.SignInProvider); signsIn && row.account != nil && row.account.Active {
			for j := range hints {
				if hints[j].action == "enter" {
					hints[j].desc = "manage"
				}
			}
		}
	}
	if m.legacyProvider() != nil {
		// First, so a narrow footer never cuts it off.
		hints = append([]footerHint{{"x", "old tokens", "x"}}, hints...)
	}
	return hints
}

// View renders the entire TUI screen purely from model state (never calls time.Now()).
func (m Model) View() string {
	if m.layout.mode == layoutTooSmall {
		msg := lipgloss.JoinVertical(lipgloss.Center,
			m.theme.text.Render("Terminal too small"),
			m.theme.dim.Render(fmt.Sprintf("need 42 %s 8", m.glyphs.times)),
		)
		return lipgloss.Place(max(1, m.layout.termW), max(1, m.layout.termH), lipgloss.Center, lipgloss.Center, msg)
	}

	header := m.renderHeader()

	if m.dlg.kind != dialogNone {
		box, _, _ := m.buildDialog()
		placed := lipgloss.Place(m.layout.termW, max(1, m.layout.termH-2), lipgloss.Center, lipgloss.Center, box)
		return header + "\n\n" + placed
	}

	footer, _ := m.renderFooter()
	return header + "\n\n" + m.viewport.View() + "\n" + footer
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
