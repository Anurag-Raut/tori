package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// barLevel categorizes a quota bar by percent left.
type barLevel int

const (
	barOK barLevel = iota
	barWarn
	barBad
)

// barCellKind represents the visual content of one cell in a quota bar.
type barCellKind int

const (
	cellTrack barCellKind = iota
	cellFill
	cellPace
)

// barState maps percent left (0..100) to a threshold state.
// > 20%: OK, 1%..20%: Warn, 0%: Bad.
func barState(leftPct float64) barLevel {
	if leftPct <= 0 {
		return barBad
	}
	if leftPct <= 20 {
		return barWarn
	}
	return barOK
}

// barCells computes the cell-by-cell layout for a quota bar of the given width.
//
// leftPct is percent of quota left in [0, 100]. Any positive quota (> 0) fills
// at least 1 cell; 100% fills all cells; 0% fills none.
//
// timeLeft is the share of the window still ahead in [0, 1], or negative when
// no pace line should be drawn (e.g. no reading yet). When >= 0, one cell at
// round(timeLeft * (width-1)) is replaced by cellPace so the line stays visible
// even when it lands on a filled cell.
func barCells(leftPct, timeLeft float64, width int) []barCellKind {
	if width <= 0 {
		return nil
	}
	leftPct = math.Max(0, math.Min(100, leftPct))

	filled := 0
	switch {
	case leftPct <= 0:
		filled = 0
	case leftPct >= 100:
		filled = width
	default:
		filled = int(math.Round(leftPct / 100.0 * float64(width)))
		if filled < 1 {
			filled = 1
		}
		if filled > width {
			filled = width
		}
	}

	cells := make([]barCellKind, width)
	for i := 0; i < filled; i++ {
		cells[i] = cellFill
	}

	if timeLeft >= 0 {
		clamped := math.Max(0, math.Min(1, timeLeft))
		idx := int(math.Round(clamped * float64(width-1)))
		if idx < 0 {
			idx = 0
		}
		if idx >= width {
			idx = width - 1
		}
		cells[idx] = cellPace
	}
	return cells
}

// renderBar turns barCells into a styled string using pre-built rowStyles.
func renderBar(cells []barCellKind, level barLevel, rs rowStyles, g glyphs, noColor bool) string {
	if len(cells) == 0 {
		return ""
	}
	fillStyle := rs.barOK
	switch level {
	case barWarn:
		fillStyle = rs.barWarn
	case barBad:
		fillStyle = rs.barBad
	}
	trackGlyph := g.barTrack
	if noColor {
		trackGlyph = g.barTrackBW
	}

	var b strings.Builder
	i := 0
	for i < len(cells) {
		kind := cells[i]
		j := i + 1
		for j < len(cells) && cells[j] == kind {
			j++
		}
		count := j - i
		switch kind {
		case cellFill:
			b.WriteString(fillStyle.Render(strings.Repeat(g.barFill, count)))
		case cellTrack:
			b.WriteString(rs.track.Render(strings.Repeat(trackGlyph, count)))
		case cellPace:
			b.WriteString(rs.pace.Render(strings.Repeat(g.pace, count)))
		}
		i = j
	}
	return b.String()
}

// formatReset formats a duration until quota reset:
// under 1h: "42m", under 1d: "1h20m" (or "1h"), otherwise "5d10h" (or "1d").
// No seconds, no spaces inside a value.
func formatReset(d time.Duration) string {
	totalMin := int(d / time.Minute)
	if d <= 0 {
		return "now"
	}
	if totalMin < 1 {
		totalMin = 1
	}
	days := totalMin / (24 * 60)
	rem := totalMin % (24 * 60)
	hours := rem / 60
	mins := rem % 60
	switch {
	case days > 0:
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// formatResetFit formats a reset duration with the reset glyph so it fits within maxW cells.
// If the exact representation exceeds maxW (e.g. "↻23h59m" in a 6-cell column), it drops
// the minor unit ("↻23h") rather than truncating mid-number.
func formatResetFit(d time.Duration, g glyphs, maxW int) string {
	s := g.reset + formatReset(d)
	if lipgloss.Width(s) <= maxW {
		return s
	}
	totalMin := int(d / time.Minute)
	days := totalMin / (24 * 60)
	hours := (totalMin % (24 * 60)) / 60
	if days > 0 {
		s = g.reset + fmt.Sprintf("%dd", days)
	} else if hours > 0 {
		s = g.reset + fmt.Sprintf("%dh", hours)
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	return ansi.Truncate(s, maxW, "")
}

// formatAge formats a reading age as "12s ago", "4m ago", or "2h ago".
func formatAge(d time.Duration) string {
	sec := int(d / time.Second)
	if sec < 0 {
		sec = 0
	}
	if sec < 60 {
		return fmt.Sprintf("%ds ago", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm ago", sec/60)
	}
	return fmt.Sprintf("%dh ago", sec/3600)
}

// formatWait formats a retry wait in seconds compactly without internal spaces.
func formatWait(sec int) string {
	if sec <= 0 {
		return "now"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return formatReset(time.Duration(sec) * time.Second)
}

// formatPct formats leftPct into a 4-cell right-aligned percent string ("100%", " 78%", "  0%").
// Any strictly positive quota (< 0.5%) displays as "1%" to match the non-zero amber state.
func formatPct(leftPct float64) string {
	if leftPct <= 0 {
		return "  0%"
	}
	if leftPct < 0.5 {
		return "  1%"
	}
	if leftPct >= 100 {
		return "100%"
	}
	return fmt.Sprintf("%3.0f%%", leftPct)
}

// windowDuration parses window labels such as "5h", "7d", "30d", or "90m".
func windowDuration(label string) (time.Duration, bool) {
	if len(label) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(label[:len(label)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch label[len(label)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	}
	return 0, false
}

// truncateEmail truncates an email in the middle of its local part while keeping
// the @domain intact whenever space permits. Measures display width using runewidth.
func truncateEmail(email string, maxW int, ellipsis string) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(email) <= maxW {
		return email
	}
	ellW := lipgloss.Width(ellipsis)
	at := strings.LastIndex(email, "@")
	if at > 0 {
		local := email[:at]
		domain := email[at:] // includes '@'
		domW := lipgloss.Width(domain)
		availLocal := maxW - domW
		if availLocal >= ellW+2 {
			budget := availLocal - ellW
			leftW := (budget + 1) / 2
			rightW := budget - leftW
			prefix := takePrefixWidth(local, leftW)
			suffix := takeSuffixWidth(local, rightW)
			out := prefix + ellipsis + suffix + domain
			if lipgloss.Width(out) <= maxW {
				return out
			}
		}
	}
	return ansi.Truncate(email, maxW, ellipsis)
}

func takePrefixWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

func takeSuffixWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	runes := []rune(s)
	w := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := runewidth.RuneWidth(runes[i])
		if w+rw > maxW {
			break
		}
		w += rw
		start = i
	}
	return string(runes[start:])
}

// fitCell truncates plain text to width cells and pads with spaces to exact width.
// Never wraps. Uses lipgloss.Width and ansi.Truncate.
func fitCell(text string, width int, rightAlign bool) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) > width {
		text = ansi.Truncate(text, width, "")
	}
	pad := width - lipgloss.Width(text)
	if pad <= 0 {
		return text
	}
	if rightAlign {
		return strings.Repeat(" ", pad) + text
	}
	return text + strings.Repeat(" ", pad)
}

// fitColumn truncates text to at most width-gap cells (with ellipsis if truncated)
// and right-pads with spaces to exact width cells, preserving gap trailing spaces.
func fitColumn(text string, width, gap int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	maxText := width - gap
	if maxText < 0 {
		maxText = 0
	}
	if lipgloss.Width(text) > maxText {
		text = ansi.Truncate(text, maxText, ellipsis)
	}
	pad := width - lipgloss.Width(text)
	if pad > 0 {
		return text + strings.Repeat(" ", pad)
	}
	return text
}
