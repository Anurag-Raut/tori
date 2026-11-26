package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Anurag-Raut/tori/internal/provider"
)

// glyphs holds the symbol set for Unicode or ASCII rendering.
type glyphs struct {
	ascii      bool
	noAnim     bool
	dot        string
	reset      string
	warn       string
	bad        string
	spinStatic string
	wait       string
	done       string
	barFill    string
	barTrack   string
	barTrackBW string
	pace       string
	dash       string
	sep        string
	ellipsis   string
	times      string
	down       string
	keyMove    string
	keyEnter   string
	keyTab     string
	toolIcons  map[string]string
}

func newGlyphs(ascii, noAnim bool) glyphs {
	if ascii {
		return glyphs{
			ascii:      true,
			noAnim:     noAnim,
			dot:        "*",
			reset:      "~",
			warn:       "!",
			bad:        "x",
			spinStatic: "...",
			wait:       ".",
			done:       "+",
			barFill:    "=",
			barTrack:   "-",
			barTrackBW: "-",
			pace:       "|",
			dash:       "-",
			sep:        "-",
			ellipsis:   "..",
			times:      "x",
			down:       "v",
			keyMove:    "j/k",
			keyEnter:   "enter",
			keyTab:     "tab",
			toolIcons: map[string]string{
				"claude":      "*",
				"codex":       "o",
				"antigravity": "^",
			},
		}
	}
	return glyphs{
		ascii:      false,
		noAnim:     noAnim,
		dot:        "●",
		reset:      "↻",
		warn:       "!",
		bad:        "✕",
		spinStatic: "…",
		wait:       "◌",
		done:       "✓",
		barFill:    "━",
		barTrack:   "━",
		barTrackBW: "─",
		pace:       "│",
		dash:       "—",
		sep:        "·",
		ellipsis:   "…",
		times:      "×",
		down:       "↓",
		keyMove:    "↑↓",
		keyEnter:   "⏎",
		keyTab:     "⇥",
		toolIcons: map[string]string{
			"claude":      "✶",
			"codex":       "⊛",
			"antigravity": "▲",
		},
	}
}

func (g glyphs) toolIcon(key string) string {
	if s, ok := g.toolIcons[key]; ok {
		return s
	}
	if g.ascii {
		return "*"
	}
	return "◆"
}

func detectASCII(env *provider.Env) bool {
	if env == nil {
		return false
	}
	if env.ASCII {
		return true
	}
	if env.Getenv("TORI_ASCII") == "1" {
		return true
	}
	if env.Getenv("TERM") == "linux" {
		return true
	}
	return false
}

func detectNoAnim(env *provider.Env) bool {
	if env == nil {
		return false
	}
	return env.Getenv("TORI_NO_ANIM") == "1"
}

func detectNoColor(env *provider.Env) bool {
	if lipgloss.ColorProfile() == termenv.Ascii {
		return true
	}
	if env != nil && env.Getenv("NO_COLOR") != "" {
		return true
	}
	return false
}

// rowStyles holds pre-built cell styles for a single row background variant
// (normal, selected, or hovered). Pre-building avoids allocating styles inside View()
// and ensures selected/hover backgrounds never break across inner foreground spans.
type rowStyles struct {
	space   lipgloss.Style
	text    lipgloss.Style
	email   lipgloss.Style
	dim     lipgloss.Style
	badge   lipgloss.Style            // plans without their own shade
	badges  map[string]lipgloss.Style // keyed by badgeKey(plan)
	good    lipgloss.Style
	warn    lipgloss.Style
	bad     lipgloss.Style
	brand   lipgloss.Style
	barOK   lipgloss.Style
	barWarn lipgloss.Style
	barBad  lipgloss.Style
	track   lipgloss.Style
	pace    lipgloss.Style
}

// theme holds all pre-built Lip Gloss styles for the TUI.
type theme struct {
	brand      lipgloss.Style
	text       lipgloss.Style
	textStrong lipgloss.Style
	dim        lipgloss.Style
	key        lipgloss.Style
	good       lipgloss.Style
	warn       lipgloss.Style
	bad        lipgloss.Style

	tools      map[string]lipgloss.Style
	toolsSel   map[string]lipgloss.Style
	toolsHover map[string]lipgloss.Style

	rowNormal   rowStyles
	rowSelected rowStyles
	rowHover    rowStyles

	dialogBox            lipgloss.Style
	dialogTitle          lipgloss.Style
	dialogChoice         lipgloss.Style
	dialogChoiceSel      lipgloss.Style
	dialogChoiceHover    lipgloss.Style
	dialogChoiceBad      lipgloss.Style
	dialogChoiceBadSel   lipgloss.Style
	dialogChoiceBadHover lipgloss.Style

	inputCursor lipgloss.Style
	inputPrompt lipgloss.Style
	inputText   lipgloss.Style
	inputPlace  lipgloss.Style
}

func newTheme(ascii bool) theme {
	// Adaptive colors: exact dark palette from spec, tuned light values (>= 4.5:1 contrast).
	// Text leaves foreground unset so the user's terminal default shows through.
	textStrongC := lipgloss.AdaptiveColor{Light: "#1a1b26", Dark: "#e6e9ff"}
	dimC := lipgloss.AdaptiveColor{Light: "#565f89", Dark: "#8089b0"}
	keyC := lipgloss.AdaptiveColor{Light: "#343b58", Dark: "#a9b1d6"}
	barOKC := lipgloss.AdaptiveColor{Light: "#6b7394", Dark: "#8a93b8"}
	trackC := lipgloss.AdaptiveColor{Light: "#d5d8e6", Dark: "#2f3452"}
	selectedC := lipgloss.AdaptiveColor{Light: "#e4e7f2", Dark: "#24283b"}
	hoverC := lipgloss.AdaptiveColor{Light: "#eef0f7", Dark: "#1f2335"}
	borderC := lipgloss.AdaptiveColor{Light: "#9aa3c8", Dark: "#3b4261"}
	badgeC := lipgloss.AdaptiveColor{Light: "#d5d8e6", Dark: "#363c58"}
	goodC := lipgloss.AdaptiveColor{Light: "#387020", Dark: "#9ece6a"}
	warnC := lipgloss.AdaptiveColor{Light: "#8c5e14", Dark: "#e0af68"}
	badC := lipgloss.AdaptiveColor{Light: "#c42847", Dark: "#f7768e"}
	brandC := lipgloss.AdaptiveColor{Light: "#6d44b8", Dark: "#bb9af7"}
	claudeC := lipgloss.AdaptiveColor{Light: "#b85318", Dark: "#ff9e64"}
	codexC := lipgloss.AdaptiveColor{Light: "#11786b", Dark: "#73daca"}
	agyC := lipgloss.AdaptiveColor{Light: "#2a57c7", Dark: "#7aa2f7"}

	// Plan badges: one shade per tier so accounts are easy to tell apart.
	type shade struct{ fg, bg lipgloss.AdaptiveColor }
	newShade := func(lightFg, lightBg, darkFg, darkBg string) shade {
		return shade{
			fg: lipgloss.AdaptiveColor{Light: lightFg, Dark: darkFg},
			bg: lipgloss.AdaptiveColor{Light: lightBg, Dark: darkBg},
		}
	}
	green := newShade("#0f5c52", "#cdebe5", "#9ee8da", "#1f4a45")
	amber := newShade("#6e4a0c", "#f3e2c0", "#f2cf95", "#54401f")
	planShades := map[string]shade{
		"free":       newShade("#454a63", "#e0e1e8", "#a4a9c0", "#33354a"),
		"go":         green,
		"plus":       green,
		"pro":        newShade("#1d438f", "#d3e0fb", "#bcd2ff", "#2d4a85"),
		"max":        newShade("#52298f", "#e6d8fb", "#dcc8ff", "#503584"),
		"team":       amber,
		"business":   amber,
		"enterprise": amber,
		"edu":        amber,
		"api":        newShade("#8a2450", "#f6d6e3", "#f6bdd3", "#5a2c42"),
	}

	buildRow := func(bg *lipgloss.AdaptiveColor, selected bool) rowStyles {
		base := lipgloss.NewStyle()
		if bg != nil {
			base = base.Background(*bg)
		}
		textBase := base
		if selected {
			textBase = textBase.Bold(true)
		}
		emailStyle := textBase
		if selected {
			emailStyle = textBase.Foreground(textStrongC)
		}
		badges := make(map[string]lipgloss.Style, len(planShades))
		for plan, s := range planShades {
			badges[plan] = textBase.Foreground(s.fg).Background(s.bg)
		}
		return rowStyles{
			badges:  badges,
			space:   base,
			text:    textBase,
			email:   emailStyle,
			dim:     textBase.Foreground(dimC),
			badge:   textBase.Foreground(keyC).Background(badgeC),
			good:    textBase.Foreground(goodC),
			warn:    textBase.Foreground(warnC),
			bad:     textBase.Foreground(badC),
			brand:   textBase.Foreground(brandC),
			barOK:   base.Foreground(barOKC),
			barWarn: base.Foreground(warnC),
			barBad:  base.Foreground(badC),
			track:   base.Foreground(trackC),
			pace:    textBase.Foreground(textStrongC),
		}
	}

	dlgBorder := lipgloss.RoundedBorder()
	if ascii {
		dlgBorder = lipgloss.ASCIIBorder()
	}

	return theme{
		brand:      lipgloss.NewStyle().Foreground(brandC).Bold(true),
		text:       lipgloss.NewStyle(),
		textStrong: lipgloss.NewStyle().Foreground(textStrongC).Bold(true),
		dim:        lipgloss.NewStyle().Foreground(dimC),
		key:        lipgloss.NewStyle().Foreground(keyC),
		good:       lipgloss.NewStyle().Foreground(goodC),
		warn:       lipgloss.NewStyle().Foreground(warnC),
		bad:        lipgloss.NewStyle().Foreground(badC),

		tools: map[string]lipgloss.Style{
			"claude":      lipgloss.NewStyle().Foreground(claudeC),
			"codex":       lipgloss.NewStyle().Foreground(codexC),
			"antigravity": lipgloss.NewStyle().Foreground(agyC),
		},
		toolsSel: map[string]lipgloss.Style{
			"claude":      lipgloss.NewStyle().Foreground(claudeC).Background(selectedC).Bold(true),
			"codex":       lipgloss.NewStyle().Foreground(codexC).Background(selectedC).Bold(true),
			"antigravity": lipgloss.NewStyle().Foreground(agyC).Background(selectedC).Bold(true),
		},
		toolsHover: map[string]lipgloss.Style{
			"claude":      lipgloss.NewStyle().Foreground(claudeC).Background(hoverC),
			"codex":       lipgloss.NewStyle().Foreground(codexC).Background(hoverC),
			"antigravity": lipgloss.NewStyle().Foreground(agyC).Background(hoverC),
		},

		rowNormal:   buildRow(nil, false),
		rowSelected: buildRow(&selectedC, true),
		rowHover:    buildRow(&hoverC, false),

		dialogBox: lipgloss.NewStyle().
			Border(dlgBorder).
			BorderForeground(borderC).
			Padding(1, 2),
		dialogTitle:          lipgloss.NewStyle().Foreground(textStrongC).Bold(true),
		dialogChoice:         lipgloss.NewStyle(),
		dialogChoiceSel:      lipgloss.NewStyle().Background(selectedC).Foreground(textStrongC).Bold(true),
		dialogChoiceHover:    lipgloss.NewStyle().Background(hoverC),
		dialogChoiceBad:      lipgloss.NewStyle().Foreground(badC),
		dialogChoiceBadSel:   lipgloss.NewStyle().Background(selectedC).Foreground(badC).Bold(true),
		dialogChoiceBadHover: lipgloss.NewStyle().Background(hoverC).Foreground(badC),

		inputCursor: lipgloss.NewStyle().Foreground(brandC),
		inputPrompt: lipgloss.NewStyle().Foreground(dimC),
		inputText:   lipgloss.NewStyle(),
		inputPlace:  lipgloss.NewStyle().Foreground(dimC),
	}
}

// badgeKey maps a plan label to its shade, e.g. "Max 5x" -> "max".
func badgeKey(plan string) string {
	first, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(plan)), " ")
	return first
}

func (rs rowStyles) planStyle(plan string) lipgloss.Style {
	if s, ok := rs.badges[badgeKey(plan)]; ok {
		return s
	}
	return rs.badge
}

func (t theme) toolStyle(key string) lipgloss.Style {
	if s, ok := t.tools[key]; ok {
		return s
	}
	return t.brand
}

func (t theme) toolStyleOnBg(key string, selected, hovered bool) lipgloss.Style {
	switch {
	case selected:
		if s, ok := t.toolsSel[key]; ok {
			return s
		}
		return t.dialogChoiceSel
	case hovered:
		if s, ok := t.toolsHover[key]; ok {
			return s
		}
		return t.dialogChoiceHover
	default:
		return t.toolStyle(key)
	}
}
