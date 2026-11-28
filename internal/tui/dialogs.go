package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Anurag-Raut/tori/internal/config"
	"github.com/Anurag-Raut/tori/internal/provider"
)

type dialogKind int

const (
	dialogNone dialogKind = iota
	dialogAddPickTool
	dialogAddSignIn
	dialogAddAlreadySaved
	dialogAddSave
	dialogAddName
	dialogInstall
	dialogTargets
	dialogAccount
	dialogCleanup
	dialogRemove
	dialogMetersOn
	dialogHelp
)

type dialogChoice struct {
	id          string
	label       string
	toolIconKey string // non-empty for provider tool picker choices
	destructive bool
}

type dialogHit struct {
	id     string
	x0, x1 int
	y      int
}

type dialogRect struct {
	x0, y0, x1, y1 int
}

// dialogState tracks active modal dialog state.
type dialogState struct {
	kind           dialogKind
	provider       provider.Provider
	account        *provider.AccountEntry
	otherSlot      int // target slot for "Switch to slot N, then remove"
	onlyAccount    bool
	status         provider.StatusInfo
	initialSlot    *int // slot signed in when Add step 2 opened
	stillSameLogin bool // true after a re-check finds the same saved account
	notFound       bool // true after a re-check finds no live login
	needName       bool // a choice was made with an empty account name
	cursor         int  // index in current dialog choices
	hoverIdx       int  // hovered choice index, -1 if none
	// then continues what the user was doing once the install succeeds.
	then  func(Model) (Model, tea.Cmd)
	files []string // old token files listed for deletion

	// The Antigravity target checklist.
	targets    []provider.Target
	checked    []bool
	title      string
	pick       func(Model, []string) (Model, tea.Cmd)
	needTarget bool // Enter was pressed with nothing checked
}

func (d *dialogState) close() {
	*d = dialogState{hoverIdx: -1}
}

// removingInUse reports whether removing the account affects the live login.
// Removing a sign-in provider's account only forgets its name.
func (d dialogState) removingInUse() bool {
	if _, ok := d.provider.(provider.SignInProvider); ok {
		return false
	}
	return d.account != nil && d.account.Active
}

// dialogChoices returns the selectable choice list for the active dialog.
func (m Model) dialogChoices() []dialogChoice {
	switch m.dlg.kind {
	case dialogAddPickTool:
		out := make([]dialogChoice, 0, len(m.providers))
		for _, p := range m.providers {
			out = append(out, dialogChoice{
				id:          "tool:" + p.Key(),
				label:       p.Title(),
				toolIconKey: p.Key(),
			})
		}
		return out

	case dialogAddAlreadySaved:
		slotN := 0
		if m.dlg.status.ManagedSlot != nil {
			slotN = *m.dlg.status.ManagedSlot
		}
		return []dialogChoice{
			{id: "update", label: fmt.Sprintf("Update slot %d with this login", slotN)},
			{id: "cancel", label: "Cancel"},
		}

	case dialogAddName:
		return []dialogChoice{
			{id: "signin", label: "Sign in with Google"},
			{id: "save", label: "Already signed in: only save the name"},
		}

	case dialogInstall:
		return []dialogChoice{
			{id: "install", label: "Install"},
			{id: "cancel", label: "Cancel"},
		}

	case dialogTargets:
		out := make([]dialogChoice, len(m.dlg.targets))
		for i, t := range m.dlg.targets {
			box := "[ ]"
			if i < len(m.dlg.checked) && m.dlg.checked[i] {
				box = "[x]"
			}
			out[i] = dialogChoice{id: "target:" + t.ID, label: fmt.Sprintf("%s %-22s %s", box, t.Name, t.Note)}
		}
		return out

	case dialogAccount:
		return []dialogChoice{
			{id: "signin", label: "Sign in again"},
			{id: "signout", label: "Sign out"},
			{id: "cancel", label: "Cancel"},
		}

	case dialogCleanup:
		// Keeping the files is default (index 0).
		return []dialogChoice{
			{id: "cancel", label: "Keep them"},
			{id: "delete", label: "Delete", destructive: true},
		}

	case dialogRemove:
		if m.dlg.account == nil {
			return nil
		}
		if !m.dlg.removingInUse() {
			// Not in use: Cancel is default (index 0).
			return []dialogChoice{
				{id: "cancel", label: "Cancel"},
				{id: "remove", label: "Remove", destructive: true},
			}
		}
		if !m.dlg.onlyAccount && m.dlg.otherSlot > 0 {
			// In use, other accounts exist: Switch then remove is default (index 0).
			return []dialogChoice{
				{id: "switch_remove", label: fmt.Sprintf("Switch to slot %d, then remove", m.dlg.otherSlot)},
				{id: "remove", label: "Remove only", destructive: true},
				{id: "cancel", label: "Cancel"},
			}
		}
		// Only account: Cancel is default (index 2). Destructive choices are never default.
		return []dialogChoice{
			{id: "remove", label: "Remove only", destructive: true},
			{id: "remove_clear", label: "Remove and clear login", destructive: true},
			{id: "cancel", label: "Cancel"},
		}

	case dialogMetersOn:
		if m.dlg.provider != nil && m.dlg.provider.Key() == "antigravity" {
			// Antigravity: Not now is default (index 0).
			return []dialogChoice{
				{id: "cancel", label: "Not now"},
				{id: "enable", label: "I understand, turn on"},
			}
		}
		// Standard disclaimer: Turn on is default (index 0).
		return []dialogChoice{
			{id: "enable", label: "Turn on"},
			{id: "cancel", label: "Not now"},
		}
	}
	return nil
}

// buildDialog renders the active dialog box, its screen-space choice hit targets,
// and its outer bounding rectangle (within the screen below the header).
func (m Model) buildDialog() (string, []dialogHit, dialogRect) {
	innerW := 44
	if m.dlg.kind == dialogTargets {
		innerW = 62
	}
	maxBoxW := max(26, m.layout.termW-4)
	if innerW+6 > maxBoxW {
		innerW = max(20, maxBoxW-6)
	}

	var lines []string
	type relHit struct {
		id   string
		line int
	}
	var relHits []relHit

	addWrapped := func(text string, st lipgloss.Style) {
		for _, ln := range wrapPlain(text, innerW) {
			lines = append(lines, st.Render(ln))
		}
	}

	addWrappedPrefix := func(prefix, text string, st lipgloss.Style) {
		pW := lipgloss.Width(prefix)
		bodyW := max(10, innerW-pW)
		indent := strings.Repeat(" ", pW)
		for i, ln := range wrapPlain(text, bodyW) {
			if i == 0 {
				lines = append(lines, st.Render(prefix+ln))
			} else {
				lines = append(lines, st.Render(indent+ln))
			}
		}
	}

	renderChoices := func(choices []dialogChoice) {
		for i, c := range choices {
			selected := i == m.dlg.cursor
			hovered := !selected && i == m.dlg.hoverIdx
			relHits = append(relHits, relHit{id: c.id, line: len(lines)})
			lines = append(lines, m.renderDialogChoice(c, innerW, selected, hovered))
		}
	}

	renderHint := func(pairs ...string) string {
		// pairs are alternating key, desc
		parts := make([]string, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			parts = append(parts, m.theme.key.Render(pairs[i])+" "+m.theme.dim.Render(pairs[i+1]))
		}
		return strings.Join(parts, " "+m.theme.dim.Render(m.glyphs.sep)+" ")
	}

	hintLine := ""

	switch m.dlg.kind {
	case dialogAddPickTool:
		lines = append(lines, m.theme.dialogTitle.Render("Add account"))
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogAddSignIn:
		title := "Add account"
		toolName := "the tool"
		if m.dlg.provider != nil {
			toolName = m.dlg.provider.Title()
			title = fmt.Sprintf("Add %s account", toolName)
		}
		lines = append(lines, m.theme.dialogTitle.Render(title))
		lines = append(lines, "")
		addWrappedPrefix("1  ", fmt.Sprintf("Sign in to %s with the account you want to add.", toolName), m.theme.text)
		addWrappedPrefix("2  ", fmt.Sprintf("Come back here and press %s.", m.glyphs.keyEnter), m.theme.text)
		if m.dlg.stillSameLogin {
			lines = append(lines, "")
			addWrappedPrefix(m.glyphs.warn+" ", "Still signed in as the saved account.", m.theme.warn)
			if slot := m.dlg.status.ManagedSlot; slot != nil {
				name := m.dlg.status.DisplayName
				meta := []string{fmt.Sprintf("Slot %d", *slot)}
				if m.dlg.provider != nil {
					for _, a := range m.data[m.dlg.provider.Key()] {
						if a.Number == *slot && a.Alias != nil && *a.Alias != "" {
							meta = append(meta, "["+*a.Alias+"]")
						}
					}
				}
				lines = append(lines, m.theme.text.Render("  "+ansi.Truncate(name, innerW-2, m.glyphs.ellipsis)))
				lines = append(lines, m.theme.dim.Render("  "+ansi.Truncate(strings.Join(meta, " "+m.glyphs.sep+" "), innerW-2, m.glyphs.ellipsis)))
			}
			addWrappedPrefix("  ", "Sign in with a different one.", m.theme.dim)
		} else if m.dlg.notFound {
			lines = append(lines, "")
			addWrappedPrefix(m.glyphs.warn+" ", "No login found yet.", m.theme.warn)
			if msg := strings.TrimSpace(m.dlg.status.Message); msg != "" {
				addWrappedPrefix("  ", msg, m.theme.dim)
			} else {
				addWrappedPrefix("  ", fmt.Sprintf("Sign in to %s and try again.", toolName), m.theme.dim)
			}
		}
		hintLine = renderHint(m.glyphs.keyEnter, "check again", "esc", "cancel")

	case dialogAddAlreadySaved:
		lines = append(lines, m.theme.dialogTitle.Render("Already saved"))
		lines = append(lines, "")
		slotN := 0
		if m.dlg.status.ManagedSlot != nil {
			slotN = *m.dlg.status.ManagedSlot
		}
		email := m.dlg.status.DisplayName
		if email == "" {
			email = "This account"
		}
		addWrapped(fmt.Sprintf("%s is slot %d.", email, slotN), m.theme.text)
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogAddSave:
		lines = append(lines, m.theme.dialogTitle.Render("Save account"))
		lines = append(lines, "")
		email := m.dlg.status.DisplayName
		if email == "" {
			email = "Signed-in account"
		}
		lines = append(lines, m.theme.text.Render(fitCell(email, innerW, false)))
		sub := ""
		if m.dlg.provider != nil {
			sub = m.dlg.provider.Title()
		}
		if m.dlg.status.Plan != "" {
			sub += " " + m.glyphs.sep + " " + m.dlg.status.Plan
		} else if m.dlg.account != nil {
			if pl := planLabel(*m.dlg.account); pl != "" {
				sub += " " + m.glyphs.sep + " " + pl
			}
		}
		if sub != "" {
			lines = append(lines, m.theme.dim.Render(fitCell(sub, innerW, false)))
		}
		lines = append(lines, "")
		lines = append(lines, m.theme.dim.Render("Alias ")+m.aliasInput.View())
		hintLine = renderHint(m.glyphs.keyEnter, "save", "esc", "cancel")

	case dialogAddName:
		toolName := ""
		if m.dlg.provider != nil {
			toolName = m.dlg.provider.Title()
		}
		lines = append(lines, m.theme.dialogTitle.Render(fmt.Sprintf("Add %s account", toolName)))
		lines = append(lines, "")
		addWrapped("Name the account. To sign in, open the link agy prints and paste the code back.", m.theme.text)
		lines = append(lines, "")
		lines = append(lines, m.theme.dim.Render("Name  ")+m.aliasInput.View())
		if m.dlg.needName {
			addWrappedPrefix(m.glyphs.warn+" ", "Enter a name first.", m.theme.warn)
		}
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		// j and k type into the name, so only the arrows move here.
		arrows := m.glyphs.keyMove
		if m.glyphs.ascii {
			arrows = "up/dn"
		}
		hintLine = renderHint(arrows, "choose", m.glyphs.keyEnter, "confirm", "esc", "cancel")

	case dialogInstall:
		sp, ok := m.dlg.provider.(provider.SignInProvider)
		if !ok {
			return "", nil, dialogRect{}
		}
		lines = append(lines, m.theme.dialogTitle.Render("No Antigravity app found"))
		lines = append(lines, "")
		addWrapped("Install the Antigravity CLI (agy) now? It also covers the Antigravity App. This runs Google's official installer, which may update your shell PATH:", m.theme.text)
		lines = append(lines, "")
		for _, ln := range wrapPlain(sp.InstallCommand(m.env), innerW-2) {
			// The address is one long word; break it rather than widen the box.
			for _, part := range strings.Split(ansi.Hardwrap(ln, innerW-2, false), "\n") {
				lines = append(lines, m.theme.dim.Render("  "+part))
			}
		}
		lines = append(lines, "")
		addWrapped("Using T3 Code? Install Antigravity in T3 Code's settings, then come back.", m.theme.dim)
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogTargets:
		lines = append(lines, m.theme.dialogTitle.Render(fitCell(m.dlg.title, innerW, false)))
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		if m.dlg.needTarget {
			lines = append(lines, "")
			addWrappedPrefix(m.glyphs.warn+" ", "Pick at least one.", m.theme.warn)
		}
		hintLine = renderHint("space", "toggle", m.glyphs.keyEnter, "sign in", "esc", "cancel")

	case dialogAccount:
		if m.dlg.account == nil {
			return "", nil, dialogRect{}
		}
		acct := *m.dlg.account
		lines = append(lines, m.theme.dialogTitle.Render(fitCell(acct.Email, innerW, false)))
		lines = append(lines, m.theme.dim.Render(fitCell(fmt.Sprintf("Slot %d %s marked signed in", acct.Number, m.glyphs.sep), innerW, false)))
		lines = append(lines, "")
		addWrapped("Sign out opens agy (type /logout if you're still signed in, then /exit). T3 Code signs out by itself.", m.theme.dim)
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogCleanup:
		lines = append(lines, m.theme.dialogTitle.Render("Delete old tokens?"))
		lines = append(lines, "")
		addWrapped("An older version of Tori saved these. They're no longer used. Account names are kept.", m.theme.text)
		lines = append(lines, "")
		const maxFiles = 6
		for i, f := range m.dlg.files {
			if i == maxFiles {
				lines = append(lines, m.theme.dim.Render(fmt.Sprintf("  and %d more", len(m.dlg.files)-maxFiles)))
				break
			}
			if rel, err := filepath.Rel(m.env.Root, f); err == nil {
				f = filepath.ToSlash(rel)
			}
			lines = append(lines, m.theme.dim.Render("  "+ansi.Truncate(f, innerW-2, m.glyphs.ellipsis)))
		}
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogRemove:
		if m.dlg.account == nil {
			return "", nil, dialogRect{}
		}
		acct := *m.dlg.account
		switch {
		case !m.dlg.removingInUse():
			lines = append(lines, m.theme.dialogTitle.Render("Remove account?"))
			lines = append(lines, "")
			lines = append(lines, m.theme.text.Render(fitCell(acct.Email, innerW, false)))
			metaBits := []string{fmt.Sprintf("Slot %d", acct.Number)}
			if acct.Alias != nil && *acct.Alias != "" {
				metaBits = append(metaBits, *acct.Alias)
			}
			if pl := planLabel(acct); pl != "" {
				metaBits = append(metaBits, pl)
			}
			lines = append(lines, m.theme.dim.Render(fitCell(strings.Join(metaBits, " "+m.glyphs.sep+" "), innerW, false)))
		case !m.dlg.onlyAccount && m.dlg.otherSlot > 0:
			lines = append(lines, m.theme.dialogTitle.Render("Remove the account in use?"))
			lines = append(lines, "")
			lines = append(lines, m.theme.text.Render(fitCell(acct.Email, innerW, false)))
		default:
			toolName := ""
			if m.dlg.provider != nil {
				toolName = m.dlg.provider.Title()
			}
			lines = append(lines, m.theme.dialogTitle.Render(fmt.Sprintf("Remove your only %s account?", toolName)))
			lines = append(lines, "")
			lines = append(lines, m.theme.text.Render(fitCell(acct.Email, innerW, false)))
		}
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogMetersOn:
		toolName := ""
		toolKey := ""
		if m.dlg.provider != nil {
			toolName = m.dlg.provider.Title()
			toolKey = m.dlg.provider.Key()
		}
		lines = append(lines, m.theme.dialogTitle.Render(fmt.Sprintf("Turn on meters for %s?", toolName)))
		lines = append(lines, "")
		if warnText, hasExtra := config.DisclaimerTool[toolKey]; hasExtra {
			addWrappedPrefix(m.glyphs.warn+" ", warnText, m.theme.warn)
			lines = append(lines, "")
			addWrapped(config.DisclaimerTail, m.theme.dim)
		} else {
			addWrapped(config.DisclaimerHead+" "+config.DisclaimerTail, m.theme.dim)
		}
		lines = append(lines, "")
		renderChoices(m.dialogChoices())
		hintLine = renderHint(m.glyphs.keyEnter, "choose", "esc", "cancel")

	case dialogHelp:
		lines = append(lines, m.theme.dialogTitle.Render("Keys"))
		lines = append(lines, "")
		moveKey := m.glyphs.keyMove + " / j k"
		if m.glyphs.ascii {
			moveKey = "up/dn / j k"
		}
		items := []struct{ k, d string }{
			{moveKey, "Move selection"},
			{m.glyphs.keyEnter, "Switch account"},
			{m.glyphs.keyTab, "Jump to next tool"},
			{"a", "Add account"},
			{"d", "Remove account"},
			{"u", "Toggle meters for tool"},
			{"b", "Rotate all tools"},
			{"r", "Refresh usage now"},
			{"q", "Quit"},
		}
		if m.legacyProvider() != nil {
			items = append(items, struct{ k, d string }{"x", "Delete old tokens"})
		}
		for _, it := range items {
			lines = append(lines, m.theme.key.Render(fitCell(it.k, 12, false))+m.theme.dim.Render(it.d))
		}
		lines = append(lines, "")
		addWrapped(fmt.Sprintf("bar = quota left %s %s = time until reset", m.glyphs.sep, m.glyphs.pace), m.theme.dim)
		hintLine = renderHint("esc", "close")

	default:
		return "", nil, dialogRect{}
	}

	if hintLine != "" {
		lines = append(lines, "", hintLine)
	}

	content := strings.Join(lines, "\n")
	box := m.theme.dialogBox.Render(content)

	boxW := lipgloss.Width(box)
	boxH := lipgloss.Height(box)
	placeH := max(1, m.layout.termH-2)
	leftX := max(0, (m.layout.termW-boxW)/2)
	topY := 2 + max(0, (placeH-boxH)/2) // header (1) + blank (1)

	// Border (1) + Padding(1, 2) => inner content starts at (+3, +2).
	innerX := leftX + 3
	innerY := topY + 2
	hits := make([]dialogHit, 0, len(relHits))
	for _, rh := range relHits {
		hits = append(hits, dialogHit{
			id: rh.id,
			x0: innerX,
			x1: innerX + innerW,
			y:  innerY + rh.line,
		})
	}
	rect := dialogRect{
		x0: leftX,
		y0: topY,
		x1: leftX + boxW,
		y1: topY + boxH,
	}
	return box, hits, rect
}

func (m Model) renderDialogChoice(c dialogChoice, innerW int, selected, hovered bool) string {
	var st lipgloss.Style
	switch {
	case c.destructive && selected:
		st = m.theme.dialogChoiceBadSel
	case c.destructive && hovered:
		st = m.theme.dialogChoiceBadHover
	case c.destructive:
		st = m.theme.dialogChoiceBad
	case selected:
		st = m.theme.dialogChoiceSel
	case hovered:
		st = m.theme.dialogChoiceHover
	default:
		st = m.theme.dialogChoice
	}

	if c.toolIconKey != "" {
		icon := m.glyphs.toolIcon(c.toolIconKey)
		iconSt := m.theme.toolStyleOnBg(c.toolIconKey, selected, hovered)
		iconW := lipgloss.Width(icon)
		restW := max(0, innerW-1-iconW-1)
		return st.Render(" ") + iconSt.Render(icon) + st.Render(" "+fitCell(c.label, restW, false))
	}

	return st.Render(" " + fitCell(c.label, max(0, innerW-1), false))
}
