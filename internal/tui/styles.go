package tui

import (
	"github.com/charmbracelet/lipgloss"
)

type providerBrand struct {
	icon      string
	color     lipgloss.Color
	iconStyle lipgloss.Style
	tabActive lipgloss.Style
	tabIdle   lipgloss.Style
	tabHover  lipgloss.Style
}

type palette struct {
	accent      lipgloss.Style
	green       lipgloss.Style
	yellow      lipgloss.Style
	red         lipgloss.Style
	text        lipgloss.Style
	bold        lipgloss.Style
	muted       lipgloss.Style
	dim         lipgloss.Style
	track       lipgloss.Style
	border      lipgloss.Style
	badge       lipgloss.Style
	keycap      lipgloss.Style
	keycapHover lipgloss.Style
	modal       lipgloss.Style
	btnOk       lipgloss.Style
	btnOkHover  lipgloss.Style
	btnNo       lipgloss.Style
	btnNoHover  lipgloss.Style
	btnDel      lipgloss.Style
	btnDelHover lipgloss.Style
	brands      map[string]providerBrand
}

func makeBrand(icon string, c lipgloss.Color, mutedC, badgeBg, hoverBg, brightC lipgloss.Color) providerBrand {
	return providerBrand{
		icon:      icon,
		color:     c,
		iconStyle: lipgloss.NewStyle().Foreground(c).Bold(true),
		tabActive: lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(c).Bold(true).Padding(0, 1),
		tabIdle:   lipgloss.NewStyle().Foreground(mutedC).Background(badgeBg).Padding(0, 1),
		tabHover:  lipgloss.NewStyle().Foreground(brightC).Background(hoverBg).Bold(true).Underline(true).Padding(0, 1),
	}
}

func (p palette) brand(key string) providerBrand {
	if b, ok := p.brands[key]; ok {
		return b
	}
	return makeBrand("◆", lipgloss.Color("141"), lipgloss.Color("245"), lipgloss.Color("238"), lipgloss.Color("242"), lipgloss.Color("255"))
}

func newPalette() palette {
	// Adaptive colors matching Tori's 256-color palette (141, 114, 179, 203, 252, 245, 239, 104, 238)
	// with clean fallback on dark/light terminals.
	accentC := lipgloss.Color("141")
	greenC := lipgloss.Color("114")
	greenHi := lipgloss.Color("156")
	yellowC := lipgloss.Color("179")
	redC := lipgloss.Color("203")
	redHi := lipgloss.Color("210")
	textC := lipgloss.Color("252")
	brightC := lipgloss.Color("255")
	mutedC := lipgloss.Color("245")
	trackC := lipgloss.Color("239")
	borderC := lipgloss.Color("104")
	badgeBg := lipgloss.Color("238")
	hoverBg := lipgloss.Color("242")

	// Distinct brand colors & icons per provider:
	// Claude Code: coral starburst (✶), Codex: emerald knot (⊛), Antigravity: sky delta (▲)
	claudeC := lipgloss.Color("209")
	codexC := lipgloss.Color("78")
	agyC := lipgloss.Color("111")

	return palette{
		accent: lipgloss.NewStyle().Foreground(accentC),
		green:  lipgloss.NewStyle().Foreground(greenC),
		yellow: lipgloss.NewStyle().Foreground(yellowC),
		red:    lipgloss.NewStyle().Foreground(redC),
		text:   lipgloss.NewStyle().Foreground(textC),
		bold:   lipgloss.NewStyle().Foreground(textC).Bold(true),
		muted:  lipgloss.NewStyle().Foreground(mutedC),
		dim:    lipgloss.NewStyle().Foreground(mutedC).Faint(true),
		track:  lipgloss.NewStyle().Foreground(trackC),
		border: lipgloss.NewStyle().Foreground(borderC),
		badge:  lipgloss.NewStyle().Foreground(textC).Background(badgeBg).Padding(0, 1),
		keycap: lipgloss.NewStyle().Reverse(true).Padding(0, 1),
		keycapHover: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(accentC).
			Bold(true).
			Padding(0, 1),
		modal: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentC).
			Padding(1, 2),
		btnOk: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(greenC).
			Bold(true).
			Padding(0, 1),
		btnOkHover: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(greenHi).
			Bold(true).
			Underline(true).
			Padding(0, 1),
		btnDel: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(redC).
			Bold(true).
			Padding(0, 1),
		btnDelHover: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(redHi).
			Bold(true).
			Underline(true).
			Padding(0, 1),
		btnNo: lipgloss.NewStyle().
			Foreground(textC).
			Background(badgeBg).
			Padding(0, 1),
		btnNoHover: lipgloss.NewStyle().
			Foreground(brightC).
			Background(hoverBg).
			Bold(true).
			Underline(true).
			Padding(0, 1),
		brands: map[string]providerBrand{
			"claude":      makeBrand("✶", claudeC, mutedC, badgeBg, hoverBg, brightC),
			"codex":       makeBrand("⊛", codexC, mutedC, badgeBg, hoverBg, brightC),
			"antigravity": makeBrand("▲", agyC, mutedC, badgeBg, hoverBg, brightC),
		},
	}
}
