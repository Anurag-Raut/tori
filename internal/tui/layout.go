package tui

const (
	minTermWidth    = 42
	minTermHeight   = 8
	wideMinWidth    = 100
	maxContentWidth = 112
	baseWideWidth   = 99
)

type layoutMode int

const (
	layoutTooSmall layoutMode = iota
	layoutNarrow
	layoutWide
)

// screenLayout holds explicit dimensions derived once from tea.WindowSizeMsg.
type screenLayout struct {
	mode     layoutMode
	termW    int
	termH    int
	contentW int // capped at 112, left-aligned
	bodyH    int // viewport height between header+blank and footer
}

func computeScreenLayout(termW, termH int) screenLayout {
	if termW < minTermWidth || termH < minTermHeight {
		return screenLayout{
			mode:  layoutTooSmall,
			termW: termW,
			termH: termH,
		}
	}
	// Screen structure: header (1) + blank (1) + body (bodyH) + footer (1).
	bodyH := termH - 3
	if bodyH < 1 {
		bodyH = 1
	}
	if termW >= wideMinWidth {
		// At 100 columns, base content width is 99 (matches section 3.1 cell table
		// and avoids right-edge deferred wrap). Extra columns grow up to 112.
		cw := termW - 1
		if cw < baseWideWidth {
			cw = baseWideWidth
		}
		if cw > maxContentWidth {
			cw = maxContentWidth
		}
		return screenLayout{
			mode:     layoutWide,
			termW:    termW,
			termH:    termH,
			contentW: cw,
			bodyH:    bodyH,
		}
	}
	return screenLayout{
		mode:     layoutNarrow,
		termW:    termW,
		termH:    termH,
		contentW: termW,
		bodyH:    bodyH,
	}
}

// wideColumns defines fixed column widths for a wide-mode tool group.
type wideColumns struct {
	rowW       int
	emailW     int
	aliasW     int
	planW      int
	barW       int
	winW       int
	winGap     int
	winCount   int
	winAreaW   int
	maxWindows int
}

// computeWideColumns computes fixed cell widths for a wide account row.
// Base (<= 2 windows, rowW=99): 1 + 2 + 2 + 23 + 11 + 7 + 25 + 2 + 25 + 1 = 99.
// When tight, <= 2 window bars shrink 10 -> 8 to keep the email column readable.
// For 3+ windows: shrinks bar 10 -> 6 -> 4, then drops alias, then drops plan.
// Any leftover width goes to the email column. Never exceeds rowW.
func computeWideColumns(rowW, numWindows int) wideColumns {
	const (
		leftPad   = 1
		dotW      = 2
		slotW     = 2
		rightPad  = 1
		baseEmail = 25
		minEmail  = 14
		tagEmail  = 21 // email width required to keep alias and plan with 3+ windows
		keepEmail = 23 // <= 2 windows: bars shrink until email gets this much
		minBar    = 8
		baseAlias = 11 // "[Personal]" + 1 gap
		basePlan  = 7  // " Free " badge + 1 gap
		winGap    = 2
		winFixed  = 4 + 1 + 4 + 2 + 6 // label(4) + gap(1) + pct(4) + flag(2) + reset(6) = 17
	)
	overhead := leftPad + dotW + slotW + rightPad // 6

	if numWindows <= 2 {
		barW := 10
		minForBase := overhead + baseEmail + baseAlias + basePlan + 2*(winFixed+barW) + winGap
		if extra := rowW - minForBase; extra >= 2 {
			add := extra / 2
			if add > 4 {
				add = 4
			}
			barW += add
		}
		for barW > minBar && rowW-overhead-baseAlias-basePlan-(2*(winFixed+barW)+winGap) < keepEmail {
			barW--
		}
		winW := winFixed + barW
		winAreaW := 2*winW + winGap
		emailW := rowW - overhead - baseAlias - basePlan - winAreaW
		if emailW < minEmail {
			emailW = minEmail
		}
		return wideColumns{
			rowW:       rowW,
			emailW:     emailW,
			aliasW:     baseAlias,
			planW:      basePlan,
			barW:       barW,
			winW:       winW,
			winGap:     winGap,
			winCount:   2,
			winAreaW:   winAreaW,
			maxWindows: 2,
		}
	}

	type candidate struct {
		barW   int
		aliasW int
		planW  int
	}
	candidates := []candidate{
		{10, baseAlias, basePlan},
		{6, baseAlias, basePlan},
		{4, baseAlias, basePlan},
		{4, 0, basePlan},
		{4, 0, 0},
	}

	k := numWindows
	for _, c := range candidates {
		winW := winFixed + c.barW
		winAreaW := k*winW + (k-1)*winGap
		needed := overhead + tagEmail + c.aliasW + c.planW + winAreaW
		if needed <= rowW {
			emailW := rowW - overhead - c.aliasW - c.planW - winAreaW
			return wideColumns{
				rowW:       rowW,
				emailW:     emailW,
				aliasW:     c.aliasW,
				planW:      c.planW,
				barW:       c.barW,
				winW:       winW,
				winGap:     winGap,
				winCount:   k,
				winAreaW:   winAreaW,
				maxWindows: k,
			}
		}
	}

	// Fallback if 4+ windows cannot fit even with bar=4 and no alias/plan:
	// cap rendered windows to what fits with minEmail.
	barW := 4
	winW := winFixed + barW
	maxK := k
	for maxK > 1 {
		winAreaW := maxK*winW + (maxK-1)*winGap
		if overhead+minEmail+winAreaW <= rowW {
			break
		}
		maxK--
	}
	winAreaW := maxK*winW + (maxK-1)*winGap
	emailW := rowW - overhead - winAreaW
	if emailW < minEmail {
		emailW = minEmail
	}
	return wideColumns{
		rowW:       rowW,
		emailW:     emailW,
		aliasW:     0,
		planW:      0,
		barW:       barW,
		winW:       winW,
		winGap:     winGap,
		winCount:   maxK,
		winAreaW:   winAreaW,
		maxWindows: maxK,
	}
}
