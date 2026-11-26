package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines all keyboard bindings for the Tori TUI using bubbles/key.
type keyMap struct {
	Up        key.Binding
	Down      key.Binding
	Switch    key.Binding
	NextTool  key.Binding
	Add       key.Binding
	Remove    key.Binding
	Meters    key.Binding
	Rotate    key.Binding
	Refresh   key.Binding
	Help      key.Binding
	Back      key.Binding
	Quit      key.Binding
	ForceQuit key.Binding
}

func newKeyMap(g glyphs) keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp(g.keyMove, "move"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp(g.keyMove, "move"),
		),
		Switch: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp(g.keyEnter, "switch"),
		),
		NextTool: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp(g.keyTab, "tool"),
		),
		Add: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "add"),
		),
		Remove: key.NewBinding(
			key.WithKeys("d", "delete"),
			key.WithHelp("d", "remove"),
		),
		Meters: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "meters"),
		),
		Rotate: key.NewBinding(
			key.WithKeys("b"),
			key.WithHelp("b", "rotate all"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "more"),
		),
		Back: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
		ForceQuit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}

type footerHint struct {
	keyLabel string
	desc     string
	action   string
}

func wideFooterHints(g glyphs) []footerHint {
	return []footerHint{
		{g.keyMove, "move", ""},
		{g.keyEnter, "switch", "enter"},
		{g.keyTab, "tool", "tab"},
		{"a", "add", "a"},
		{"d", "remove", "d"},
		{"u", "meters", "u"},
		{"b", "rotate all", "b"},
		{"r", "refresh", "r"},
		{"q", "quit", "q"},
	}
}

func narrowFooterHints(g glyphs) []footerHint {
	return []footerHint{
		{g.keyEnter, "switch", "enter"},
		{"a", "add", "a"},
		{"d", "remove", "d"},
		{"?", "more", "?"},
	}
}
