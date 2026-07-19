package studio

import "charm.land/bubbles/v2/key"

// studioKeyMap drives the Charm help bubble (short footer + full help view).
type studioKeyMap struct {
	Search  key.Binding
	Focus   key.Binding
	Filters key.Binding
	Mode    key.Binding
	Index   key.Binding
	Similar key.Binding
	Yank    key.Binding
	Open    key.Binding
	Status  key.Binding
	Config  key.Binding
	Help    key.Binding
	Quit    key.Binding
	Confirm key.Binding
	Cancel  key.Binding
}

func defaultStudioKeys() studioKeyMap {
	return studioKeyMap{
		Search: key.NewBinding(
			key.WithKeys("enter", "/"),
			key.WithHelp("enter//", "search · filter hits"),
		),
		Focus: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "focus"),
		),
		Filters: key.NewBinding(
			key.WithKeys("f"),
			key.WithHelp("f", "filters"),
		),
		Mode: key.NewBinding(
			key.WithKeys("m"),
			key.WithHelp("m", "mode"),
		),
		Index: key.NewBinding(
			key.WithKeys("r", "R"),
			key.WithHelp("r/R", "index"),
		),
		Similar: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "similar"),
		),
		Yank: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "yank"),
		),
		Open: key.NewBinding(
			key.WithKeys("o", "enter"),
			key.WithHelp("o", "open"),
		),
		Status: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "status"),
		),
		Config: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "config"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "q"),
			key.WithHelp("ctrl+c", "quit"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "confirm"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("n", "esc"),
			key.WithHelp("n/esc", "cancel"),
		),
	}
}

func (k studioKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Search, k.Filters, k.Index, k.Yank, k.Help, k.Quit}
}

func (k studioKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Search, k.Focus, k.Filters, k.Mode},
		{k.Index, k.Similar, k.Yank, k.Open},
		{k.Status, k.Config, k.Help, k.Quit},
		{k.Confirm, k.Cancel},
	}
}
