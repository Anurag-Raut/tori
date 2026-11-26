package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Anurag-Raut/tori/internal/provider"
)

// renderToolHeader renders a plain tool group header with no borders.
// When meters are off, shows "meters off" two cells after the name.
func renderToolHeader(p provider.Provider, metersOn bool, lay screenLayout, th theme) string {
	name := th.toolStyle(p.Key()).Render(p.Title())
	left := " " + name
	if !metersOn {
		left += "  " + th.dim.Render("meters off")
		return fitCell(left, lay.contentW, false)
	}
	return left
}

// renderEmptyToolRow renders the selectable placeholder line for a tool with no accounts.
func renderEmptyToolRow(rs rowStyles, width int, g glyphs) string {
	msg := fmt.Sprintf("no accounts %s press a to add one", g.sep)
	innerW := max(0, width-2)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		rs.space.Render(" "),
		rs.dim.Render(fitCell(msg, innerW, false)),
		rs.space.Render(" "),
	)
}

// renderErrorToolRow renders the selectable error line when a tool failed to load.
func renderErrorToolRow(rs rowStyles, width int, g glyphs) string {
	msg := fmt.Sprintf("%s couldn't read accounts %s r to retry", g.bad, g.sep)
	innerW := max(0, width-2)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		rs.space.Render(" "),
		rs.bad.Render(fitCell(msg, innerW, false)),
		rs.space.Render(" "),
	)
}

// renderWideAccountRow renders one account as a single horizontal line of fixed-width cells.
func renderWideAccountRow(
	acct provider.AccountEntry,
	state accountRowState,
	cols wideColumns,
	rs rowStyles,
	g glyphs,
	noColor bool,
	spinView string,
	now time.Time,
) string {
	cells := make([]string, 0, 8)
	cells = append(cells, rs.space.Render(" "))

	// Active dot (1 + 1 gap)
	if acct.Active {
		cells = append(cells, rs.good.Render(g.dot), rs.space.Render(" "))
	} else {
		cells = append(cells, rs.space.Render("  "))
	}

	// Slot (1 + 1 gap; accommodates 2-digit slots cleanly)
	slotStr := strconv.Itoa(acct.Number)
	cells = append(cells, rs.dim.Render(fitCell(slotStr, 2, false)))

	// Email (keep 1-cell breathing room before next column)
	emailText := truncateEmail(acct.Email, max(1, cols.emailW-1), g.ellipsis)
	cells = append(cells, rs.email.Render(fitCell(emailText, cols.emailW, false)))

	// Alias (dim, blank if none)
	if cols.aliasW > 0 {
		alias := ""
		if acct.Alias != nil {
			alias = *acct.Alias
		}
		cells = append(cells, rs.dim.Render(fitColumn(alias, cols.aliasW, 1, g.ellipsis)))
	}

	// Plan (dim, blank if none, "API" for API-key accounts)
	if cols.planW > 0 {
		plan := planLabel(acct)
		cells = append(cells, rs.dim.Render(fitColumn(plan, cols.planW, 1, g.ellipsis)))
	}

	// Window area (54 cells in standard 2-window layout)
	cells = append(cells, renderWideWindowArea(acct, state, cols, rs, g, noColor, spinView, now))

	cells = append(cells, rs.space.Render(" "))
	return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
}

func renderWideWindowArea(
	acct provider.AccountEntry,
	state accountRowState,
	cols wideColumns,
	rs rowStyles,
	g glyphs,
	noColor bool,
	spinView string,
	now time.Time,
) string {
	areaW := cols.winAreaW
	switch state {
	case stateMetersOff:
		return rs.space.Render(strings.Repeat(" ", areaW))

	case stateAPIKey:
		return rs.dim.Render(fitCell("no usage for API keys", areaW, false))

	case stateReading:
		spin := spinView
		if g.noAnim {
			spin = g.spinStatic
		}
		spinW := lipgloss.Width(spin)
		rest := fitCell(" reading"+g.spinStatic, max(0, areaW-spinW), false)
		return rs.brand.Render(spin) + rs.dim.Render(rest)

	case stateNoReading:
		return renderWideWindowCells(defaultNoReadingWindows(), cols, rs, g, noColor)

	case stateLoginExpired:
		msg := fmt.Sprintf("%s login expired %s press a to sign in again", g.bad, g.sep)
		return rs.bad.Render(fitCell(msg, areaW, false))

	case stateRateLimited:
		waitText := " cooling down"
		if acct.Usage != nil && acct.Usage.RetryAfterSeconds != nil && *acct.Usage.RetryAfterSeconds > 0 {
			waitText = fmt.Sprintf(" cooling down %s retry in %s", g.sep, formatWait(*acct.Usage.RetryAfterSeconds))
		}
		iconW := lipgloss.Width(g.wait)
		return rs.warn.Render(g.wait) + rs.dim.Render(fitCell(waitText, max(0, areaW-iconW), false))

	case stateOutdated:
		views := buildWindowViews(acct.Usage, now)
		age, _ := usageAge(acct.Usage, now)
		msg := formatSummaryLine(views, fmt.Sprintf("read %s", formatAge(age)), g)
		return rs.dim.Render(fitCell(msg, areaW, false))

	case stateOffline:
		views := buildWindowViews(acct.Usage, now)
		age, hasAge := usageAge(acct.Usage, now)
		tail := "offline"
		if hasAge {
			tail = fmt.Sprintf("offline, read %s", formatAge(age))
		}
		msg := formatSummaryLine(views, tail, g)
		return rs.dim.Render(fitCell(msg, areaW, false))

	case stateError:
		errText := "error"
		if acct.Usage != nil && acct.Usage.Error != nil && *acct.Usage.Error != "" {
			errText = *acct.Usage.Error
		}
		views := buildWindowViews(acct.Usage, now)
		if len(views) > 0 {
			msg := formatSummaryLine(views, errText, g)
			return rs.dim.Render(fitCell(msg, areaW, false))
		}
		return rs.bad.Render(fitCell(g.bad+" "+errText, areaW, false))

	default: // stateHealthy, stateRunningLow, stateExhausted
		views := buildWindowViews(acct.Usage, now)
		return renderWideWindowCells(views, cols, rs, g, noColor)
	}
}

func renderWideWindowCells(views []windowView, cols wideColumns, rs rowStyles, g glyphs, noColor bool) string {
	limit := min(len(views), cols.maxWindows)
	parts := make([]string, 0, limit*2+1)
	usedW := 0
	for i := 0; i < limit; i++ {
		if i > 0 {
			parts = append(parts, rs.space.Render(strings.Repeat(" ", cols.winGap)))
			usedW += cols.winGap
		}
		parts = append(parts, renderSingleWideWindow(views[i], cols.barW, rs, g, noColor))
		usedW += cols.winW
	}
	if rem := cols.winAreaW - usedW; rem > 0 {
		parts = append(parts, rs.space.Render(strings.Repeat(" ", rem)))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// renderSingleWideWindow renders a 17+barW cell window (27 cells when barW=10):
// label(4) + bar(barW) + gap(1) + pct(4) + flag(2) + reset(6).
func renderSingleWideWindow(v windowView, barW int, rs rowStyles, g glyphs, noColor bool) string {
	label := rs.dim.Render(fitCell(v.label, 3, false) + " ")

	if v.noReading {
		cells := barCells(0, -1, barW)
		bar := renderBar(cells, barOK, rs, g, noColor)
		pct := rs.dim.Render(fitCell(g.dash, 4, true))
		return lipgloss.JoinHorizontal(lipgloss.Top,
			label,
			bar,
			rs.space.Render(" "),
			pct,
			rs.space.Render(strings.Repeat(" ", 2+6)),
		)
	}

	cells := barCells(v.leftPct, v.timeLeft, barW)
	bar := renderBar(cells, v.level, rs, g, noColor)

	pctStr := formatPct(v.leftPct)
	pctStyle := rs.text
	flagCell := rs.space.Render("  ")
	switch v.level {
	case barWarn:
		pctStyle = rs.warn
		flagCell = rs.warn.Render(fitCell(g.warn, 2, false))
	case barBad:
		pctStyle = rs.bad
		flagCell = rs.bad.Render(fitCell(g.bad, 2, false))
	}
	pct := pctStyle.Render(fitCell(pctStr, 4, true))

	resetStr := ""
	if v.hasReset {
		resetStr = formatResetFit(v.resetDur, g, 6)
	}
	reset := rs.dim.Render(fitCell(resetStr, 6, false))

	return lipgloss.JoinHorizontal(lipgloss.Top,
		label,
		bar,
		rs.space.Render(" "),
		pct,
		flagCell,
		reset,
	)
}

func formatSummaryLine(views []windowView, tail string, g glyphs) string {
	if len(views) == 0 {
		return tail
	}
	bits := make([]string, 0, len(views))
	for _, v := range views {
		bits = append(bits, fmt.Sprintf("%s %s", v.label, strings.TrimSpace(formatPct(v.leftPct))))
	}
	return strings.Join(bits, " "+g.sep+" ") + "  " + g.sep + "  " + tail
}

// renderNarrowAccountRow renders an account in Narrow mode (42..99 cols).
// Unselected accounts are a single collapsed line showing the lowest window left.
// The selected account expands into a tinted multi-line block with alias, plan, and all windows.
func renderNarrowAccountRow(
	acct provider.AccountEntry,
	state accountRowState,
	selected bool,
	rowW int,
	rs rowStyles,
	g glyphs,
	noColor bool,
	spinView string,
	now time.Time,
) string {
	if !selected {
		return renderNarrowCollapsed(acct, state, rowW, rs, g, noColor, spinView, now)
	}
	return renderNarrowExpanded(acct, state, rowW, rs, g, noColor, spinView, now)
}

func renderNarrowCollapsed(
	acct provider.AccountEntry,
	state accountRowState,
	rowW int,
	rs rowStyles,
	g glyphs,
	noColor bool,
	spinView string,
	now time.Time,
) string {
	const (
		leftPad  = 1
		dotW     = 2
		slotW    = 2
		rightPad = 1
	)
	dot := rs.space.Render("  ")
	if acct.Active {
		dot = rs.good.Render(g.dot) + rs.space.Render(" ")
	}
	slot := rs.dim.Render(fitCell(strconv.Itoa(acct.Number), 2, false))

	rightPart := ""
	rightW := 0

	switch state {
	case stateMetersOff:
		// Nothing on the right.
	case stateAPIKey:
		rightPart = rs.dim.Render("API")
		rightW = 3
	case stateReading:
		spin := spinView
		if g.noAnim {
			spin = g.spinStatic
		}
		rightPart = rs.brand.Render(spin)
		rightW = lipgloss.Width(spin)
	case stateNoReading:
		bar := renderBar(barCells(0, -1, 6), barOK, rs, g, noColor)
		rightPart = bar + rs.space.Render(" ") + rs.dim.Render(fitCell(g.dash, 4, true))
		rightW = 11
	case stateLoginExpired:
		txt := g.bad + " expired"
		rightPart = rs.bad.Render(txt)
		rightW = lipgloss.Width(txt)
	case stateRateLimited:
		txt := "wait"
		if acct.Usage != nil && acct.Usage.RetryAfterSeconds != nil && *acct.Usage.RetryAfterSeconds > 0 {
			txt = formatWait(*acct.Usage.RetryAfterSeconds)
		}
		rightPart = rs.warn.Render(g.wait) + rs.dim.Render(" "+txt)
		rightW = lipgloss.Width(g.wait) + 1 + lipgloss.Width(txt)
	case stateOffline:
		views := buildWindowViews(acct.Usage, now)
		if low, ok := lowestWindow(views); ok {
			txt := strings.TrimSpace(formatPct(low.leftPct)) + " offline"
			rightPart = rs.dim.Render(txt)
			rightW = lipgloss.Width(txt)
		} else {
			rightPart = rs.dim.Render("offline")
			rightW = 7
		}
	case stateOutdated:
		views := buildWindowViews(acct.Usage, now)
		if low, ok := lowestWindow(views); ok {
			txt := strings.TrimSpace(formatPct(low.leftPct)) + " old"
			rightPart = rs.dim.Render(txt)
			rightW = lipgloss.Width(txt)
		}
	case stateError:
		txt := g.bad + " err"
		rightPart = rs.bad.Render(txt)
		rightW = lipgloss.Width(txt)
	default: // stateHealthy, stateRunningLow, stateExhausted
		views := buildWindowViews(acct.Usage, now)
		if low, ok := lowestWindow(views); ok {
			bar := renderBar(barCells(low.leftPct, low.timeLeft, 6), low.level, rs, g, noColor)
			pctStr := formatPct(low.leftPct)
			pctStyle := rs.text
			flag := ""
			flagW := 0
			switch low.level {
			case barWarn:
				pctStyle = rs.warn
				flag = rs.warn.Render(g.warn)
				flagW = lipgloss.Width(g.warn)
			case barBad:
				pctStyle = rs.bad
				flag = rs.bad.Render(g.bad)
				flagW = lipgloss.Width(g.bad)
			}
			rightPart = bar + rs.space.Render(" ") + pctStyle.Render(pctStr) + flag
			rightW = 6 + 1 + 4 + flagW
		}
	}

	availEmail := rowW - leftPad - dotW - slotW - rightPad
	gapW := 0
	if rightW > 0 {
		gapW = 1
		availEmail -= rightW + gapW
	}
	if availEmail < 4 {
		availEmail = 4
	}
	emailText := truncateEmail(acct.Email, availEmail, g.ellipsis)
	emailCell := rs.email.Render(fitCell(emailText, availEmail+gapW, false))

	parts := []string{
		rs.space.Render(" "),
		dot,
		slot,
		emailCell,
	}
	if rightPart != "" {
		parts = append(parts, rightPart)
	}
	parts = append(parts, rs.space.Render(" "))
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func renderNarrowExpanded(
	acct provider.AccountEntry,
	state accountRowState,
	rowW int,
	rs rowStyles,
	g glyphs,
	noColor bool,
	spinView string,
	now time.Time,
) string {
	const (
		leftPad  = 1
		dotW     = 2
		slotW    = 2
		rightPad = 1
		indentW  = 5 // 1 left pad + 4 indent, aligns under email
	)
	var lines []string

	// Line 1: dot, slot, email (bold + TextStrong)
	dot := rs.space.Render("  ")
	if acct.Active {
		dot = rs.good.Render(g.dot) + rs.space.Render(" ")
	}
	slot := rs.dim.Render(fitCell(strconv.Itoa(acct.Number), 2, false))
	emailW := max(4, rowW-leftPad-dotW-slotW-rightPad)
	emailText := truncateEmail(acct.Email, emailW, g.ellipsis)
	lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
		rs.space.Render(" "),
		dot,
		slot,
		rs.email.Render(fitCell(emailText, emailW, false)),
		rs.space.Render(" "),
	))

	innerW := max(4, rowW-indentW-rightPad)
	indent := rs.space.Render(strings.Repeat(" ", indentW))
	rPad := rs.space.Render(" ")

	// Line 2: alias · plan (if either is present)
	var metaParts []string
	if acct.Alias != nil && *acct.Alias != "" {
		metaParts = append(metaParts, *acct.Alias)
	}
	if pl := planLabel(acct); pl != "" {
		metaParts = append(metaParts, pl)
	}
	if len(metaParts) > 0 {
		metaText := strings.Join(metaParts, " "+g.sep+" ")
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			indent,
			rs.dim.Render(fitCell(metaText, innerW, false)),
			rPad,
		))
	}

	// Window / state detail lines
	switch state {
	case stateMetersOff:
		// No extra lines.
	case stateAPIKey:
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			indent,
			rs.dim.Render(fitCell("no usage for API keys", innerW, false)),
			rPad,
		))
	case stateReading:
		spin := spinView
		if g.noAnim {
			spin = g.spinStatic
		}
		spinW := lipgloss.Width(spin)
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			indent,
			rs.brand.Render(spin),
			rs.dim.Render(fitCell(" reading"+g.spinStatic, max(0, innerW-spinW), false)),
			rPad,
		))
	case stateNoReading:
		for _, v := range defaultNoReadingWindows() {
			lines = append(lines, renderNarrowWindowLine(v, innerW, indent, rPad, rs, g, noColor))
		}
	case stateLoginExpired:
		for _, seg := range wrapPlain(fmt.Sprintf("%s login expired %s press a to sign in again", g.bad, g.sep), innerW) {
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
				indent,
				rs.bad.Render(fitCell(seg, innerW, false)),
				rPad,
			))
		}
	case stateRateLimited:
		msg := " cooling down"
		if acct.Usage != nil && acct.Usage.RetryAfterSeconds != nil && *acct.Usage.RetryAfterSeconds > 0 {
			msg = fmt.Sprintf(" cooling down %s retry in %s", g.sep, formatWait(*acct.Usage.RetryAfterSeconds))
		}
		iconW := lipgloss.Width(g.wait)
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			indent,
			rs.warn.Render(g.wait),
			rs.dim.Render(fitCell(msg, max(0, innerW-iconW), false)),
			rPad,
		))
	case stateOutdated:
		views := buildWindowViews(acct.Usage, now)
		age, _ := usageAge(acct.Usage, now)
		for _, seg := range wrapPlain(formatSummaryLine(views, fmt.Sprintf("read %s", formatAge(age)), g), innerW) {
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
				indent,
				rs.dim.Render(fitCell(seg, innerW, false)),
				rPad,
			))
		}
	case stateOffline:
		views := buildWindowViews(acct.Usage, now)
		age, hasAge := usageAge(acct.Usage, now)
		tail := "offline"
		if hasAge {
			tail = fmt.Sprintf("offline, read %s", formatAge(age))
		}
		for _, seg := range wrapPlain(formatSummaryLine(views, tail, g), innerW) {
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
				indent,
				rs.dim.Render(fitCell(seg, innerW, false)),
				rPad,
			))
		}
	case stateError:
		errText := "error"
		if acct.Usage != nil && acct.Usage.Error != nil && *acct.Usage.Error != "" {
			errText = *acct.Usage.Error
		}
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
			indent,
			rs.bad.Render(fitCell(g.bad+" "+errText, innerW, false)),
			rPad,
		))
	default: // stateHealthy, stateRunningLow, stateExhausted
		for _, v := range buildWindowViews(acct.Usage, now) {
			lines = append(lines, renderNarrowWindowLine(v, innerW, indent, rPad, rs, g, noColor))
		}
	}

	return strings.Join(lines, "\n")
}

func renderNarrowWindowLine(v windowView, innerW int, indent, rPad string, rs rowStyles, g glyphs, noColor bool) string {
	const barW = 10
	const labelW = 4
	label := rs.dim.Render(fitCell(v.label, 3, false) + " ")

	if v.noReading {
		bar := renderBar(barCells(0, -1, barW), barOK, rs, g, noColor)
		pct := rs.dim.Render(fitCell(g.dash, 4, true))
		used := labelW + barW + 1 + 4
		tail := ""
		if rem := innerW - used; rem > 0 {
			tail = rs.space.Render(strings.Repeat(" ", rem))
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, indent, label, bar, rs.space.Render(" "), pct, tail, rPad)
	}

	bar := renderBar(barCells(v.leftPct, v.timeLeft, barW), v.level, rs, g, noColor)
	pctStr := formatPct(v.leftPct)
	pctStyle := rs.text
	flagStr := ""
	flagStyled := ""
	switch v.level {
	case barWarn:
		pctStyle = rs.warn
		flagStr = g.warn
		flagStyled = rs.warn.Render(g.warn)
	case barBad:
		pctStyle = rs.bad
		flagStr = g.bad
		flagStyled = rs.bad.Render(g.bad)
	}
	pct := pctStyle.Render(fitCell(pctStr, 4, true))

	used := labelW + barW + 1 + 4 + lipgloss.Width(flagStr)
	resetPart := ""
	if v.hasReset {
		resetText := " " + g.reset + formatReset(v.resetDur)
		rem := innerW - used
		if rem > 0 {
			if lipgloss.Width(resetText) > rem {
				resetText = " " + formatResetFit(v.resetDur, g, max(1, rem-1))
			}
			resetPart = rs.dim.Render(fitCell(resetText, rem, false))
			used += rem
		}
	}
	tail := ""
	if rem := innerW - used; rem > 0 {
		tail = rs.space.Render(strings.Repeat(" ", rem))
	}

	parts := []string{indent, label, bar, rs.space.Render(" "), pct}
	if flagStyled != "" {
		parts = append(parts, flagStyled)
	}
	if resetPart != "" {
		parts = append(parts, resetPart)
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	parts = append(parts, rPad)
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// wrapPlain wraps plain text on word boundaries to at most maxW display cells per line.
func wrapPlain(s string, maxW int) []string {
	if maxW <= 0 {
		return []string{""}
	}
	if lipgloss.Width(s) <= maxW {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur)+1+lipgloss.Width(w) <= maxW {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	lines = append(lines, cur)
	return lines
}
