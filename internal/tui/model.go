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
	key   string
	label string
}

var footerKeys = []keyItem{
	{"↑↓", "move"},
	{"⏎", "switch"},
	{"⇥", "tool"},
	{"a", "add"},
	{"d", "remove"},
	{"u", "usage"},
	{"b", "rotate"},
	{"r", "refresh"},
	{"q", "quit"},
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
	aliasInput    textinput.Model
	spinner       spinner.Model
	styles        palette

	lastClickRow int
	lastClickAt  time.Time
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

func (m Model) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.modal {
	case modalAdd:
		switch msg.String() {
		case "esc":
			m.modal = modalNone
			m.aliasInput.Blur()
			m.statusMsg = "Cancelled."
			m.statusTone = toneYellow
			return m, nil
		case "enter":
			alias := m.aliasInput.Value()
			p := m.modalProvider
			m.modal = modalNone
			m.aliasInput.Blur()
			m.statusMsg = "Saving account..."
			m.statusTone = toneYellow
			return m, addCmd(m.env, p, alias)
		default:
			var cmd tea.Cmd
			m.aliasInput, cmd = m.aliasInput.Update(msg)
			return m, cmd
		}

	case modalDelete:
		switch msg.String() {
		case "y", "Y":
			p := m.modalProvider
			acct := *m.modalAccount
			m.modal = modalNone
			m.modalAccount = nil
			return m, removeCmd(m.env, p, acct.Number, acct)
		case "n", "N", "esc":
			m.modal = modalNone
			m.modalAccount = nil
			m.statusMsg = "Cancelled."
			m.statusTone = toneYellow
			return m, nil
		}

	case modalDisclaimer:
		switch msg.String() {
		case "y", "Y", "enter":
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
		case "n", "N", "esc":
			m.modal = modalNone
			m.statusMsg = "Cancelled."
			m.statusTone = toneYellow
			return m, nil
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
	m.modal = modalAdd
	m.modalProvider = p
	m.aliasInput.Reset()
	if p.Key() == "antigravity" {
		m.aliasInput.Placeholder = "alias (optional, e.g. primary)"
	} else {
		m.aliasInput.Placeholder = "optional alias (Enter for none)"
	}
	cmd := m.aliasInput.Focus()
	return m, cmd
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.modal != modalNone {
		return m, nil
	}
	flat := m.rows()
	n := len(flat)
	if n == 0 {
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
		m.lastClickRow = rowIdx
		m.lastClickAt = now
		m.syncScroll()

		if isDouble {
			m.lastClickRow = -1
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
		return m, nil
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
	footerHeight int
}

func (m Model) renderFooter() []string {
	w := m.width
	gap := 2
	if w < 90 {
		gap = 1
	}
	var rows []string
	var cur strings.Builder
	cur.WriteString(" ")
	x := 1
	for _, item := range footerKeys {
		capStr := m.styles.keycap.Render(item.key)
		labStr := " " + m.styles.muted.Render(item.label)
		itemStr := capStr + labStr
		need := lipgloss.Width(itemStr)
		if x+need > w-1 && x > 1 {
			rows = append(rows, cur.String())
			cur.Reset()
			cur.WriteString(" ")
			x = 1
		}
		if x > 1 {
			cur.WriteString(strings.Repeat(" ", gap))
			x += gap
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
	return rows
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
	footerLines := m.renderFooter()
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
		titlePart := m.styles.accent.Bold(true).Render(" "+p.Title()) +
			m.styles.muted.Render(count) +
			m.styles.dim.Render(tag+" ")
		titleW := lipgloss.Width(titlePart)
		dashW := max(0, inner-1-titleW)
		top := " " + m.styles.border.Render("╭─") + titlePart + m.styles.border.Render(strings.Repeat("─", dashW)+"╮")
		lines = append(lines, top)

		if errText != "" || len(accts) == 0 {
			sel := idx == m.cur
			rowToLine[idx] = len(lines)
			lineToRow[len(lines)] = idx
			mark := " "
			if sel {
				mark = m.styles.accent.Bold(true).Render("▌")
			}
			var msg string
			if errText != "" {
				msg = m.styles.red.Render(truncateRunes("error: "+errText, max(10, inner-4)))
			} else {
				st := m.styles.muted
				if sel {
					st = m.styles.text
				}
				msg = st.Render(truncateRunes("no accounts yet — press a to save the signed-in login", max(10, inner-4)))
			}
			lines = append(lines, boxed(" "+mark+" "+msg))
			idx++
		} else {
			for _, a := range accts {
				sel := idx == m.cur
				rowToLine[idx] = len(lines)
				lineToRow[len(lines)] = idx

				mark := " "
				if sel {
					mark = m.styles.accent.Bold(true).Render("▌")
				}
				dot := m.styles.track.Render("○")
				if a.Active {
					dot = m.styles.green.Render("●")
				}
				nameStyle := m.styles.muted
				if a.Active || sel {
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
		footerHeight: len(footerLines),
	}
}

func (m Model) renderModal() string {
	bw := max(32, min(m.width-4, 76))
	switch m.modal {
	case modalAdd:
		title := m.styles.accent.Bold(true).Render(fmt.Sprintf("Add %s account", m.modalProvider.Title()))
		hint := m.styles.muted.Render("Saves the currently signed-in login.")
		if m.modalProvider.Key() == "antigravity" {
			hint = m.styles.muted.Render("Saves the signed-in Antigravity login (alias optional; email loads with usage).")
		}
		inputBox := m.aliasInput.View()
		btns := m.styles.btnOk.Render("Enter: Save") + "  " + m.styles.btnNo.Render("Esc: Cancel")
		content := lipgloss.JoinVertical(lipgloss.Left, title, hint, "", inputBox, "", btns)
		return m.styles.modal.Width(min(bw, 60)).Render(content)

	case modalDelete:
		title := m.styles.red.Bold(true).Render(fmt.Sprintf("Remove %s slot %d?", m.modalProvider.Title(), m.modalAccount.Number))
		who := m.styles.bold.Render(m.modalAccount.Email)
		if m.modalAccount.Alias != nil && *m.modalAccount.Alias != "" {
			who += " " + m.styles.muted.Render("["+*m.modalAccount.Alias+"]")
		}
		note := m.styles.muted.Render("This only forgets the saved login; remaining slots are renumbered.")
		btns := m.styles.btnDel.Render("y: Remove") + "  " + m.styles.btnNo.Render("n / Esc: Cancel")
		content := lipgloss.JoinVertical(lipgloss.Left, title, "", who, note, "", btns)
		return m.styles.modal.Width(min(bw, 62)).Render(content)

	case modalDisclaimer:
		title := m.styles.accent.Bold(true).Render(fmt.Sprintf("Turn on usage meters for %s?", m.modalProvider.Title()))
		paras := config.DisclaimerLines([]string{m.modalProvider.Key()})
		bodyW := min(bw, 72) - 6
		if bodyW < 24 {
			bodyW = 24
		}
		var wrapped []string
		for _, p := range paras {
			if p == config.DisclaimerTail {
				wrapped = append(wrapped, "")
			}
			wrapped = append(wrapped, lipgloss.NewStyle().Width(bodyW).Render(p))
			if !strings.HasPrefix(p, "- ") && p != config.DisclaimerTerms {
				wrapped = append(wrapped, "")
			}
		}
		body := m.styles.text.Render(strings.TrimRight(strings.Join(wrapped, "\n"), "\n"))
		btns := m.styles.btnOk.Render("y / Enter: Accept & Turn On") + "  " + m.styles.btnNo.Render("n / Esc: Cancel")
		content := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", btns)
		return m.styles.modal.Width(min(bw, 74)).Render(content)
	}
	return ""
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
		modalBox := m.renderModal()
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
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithOutput(os.Stdout))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tori: %v\n", err)
		return 1
	}
	return 0
}
