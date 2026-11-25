package tui

import (
	"github.com/charmbracelet/lipgloss"
)

type palette struct {
	accent lipgloss.Style
	green  lipgloss.Style
	yellow lipgloss.Style
	red    lipgloss.Style
	text   lipgloss.Style
	bold   lipgloss.Style
	muted  lipgloss.Style
	dim    lipgloss.Style
	track  lipgloss.Style
	border lipgloss.Style
	badge  lipgloss.Style
	keycap lipgloss.Style
	modal  lipgloss.Style
	btnOk  lipgloss.Style
	btnNo  lipgloss.Style
	btnDel lipgloss.Style
}

func newPalette() palette {
	// Adaptive colors matching Tori's 256-color palette (141, 114, 179, 203, 252, 245, 239, 104, 238)
	// with clean fallback on dark/light terminals.
	accentC := lipgloss.Color("141")
	greenC := lipgloss.Color("114")
	yellowC := lipgloss.Color("179")
	redC := lipgloss.Color("203")
	textC := lipgloss.Color("252")
	mutedC := lipgloss.Color("245")
	trackC := lipgloss.Color("239")
	borderC := lipgloss.Color("104")
	badgeBg := lipgloss.Color("238")

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
		modal: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentC).
			Padding(1, 2),
		btnOk: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(greenC).
			Bold(true).
			Padding(0, 1),
		btnDel: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(redC).
			Bold(true).
			Padding(0, 1),
		btnNo: lipgloss.NewStyle().
			Foreground(textC).
			Background(badgeBg).
			Padding(0, 1),
	}
}
