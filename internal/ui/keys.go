package ui

import "charm.land/bubbles/v2/key"

// KeyMap is the full set of bindings. Grouping them here keeps the help
// footer and the Update switch from drifting apart.
type KeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
	Back    key.Binding
	New     key.Binding
	Start   key.Binding
	Stop    key.Binding
	Restart key.Binding
	Delete  key.Binding
	Console key.Binding
	Refresh key.Binding
	Command key.Binding
	Help    key.Binding
	Quit    key.Binding
}

// DefaultKeyMap returns the standard bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "details")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		New:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new VM")),
		Start:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		Stop:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		Restart: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Delete:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Console: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "console cmd")),
		Refresh: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh")),
		Command: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "show qemu cmd")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp is the one-line footer binding set.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.New, k.Enter, k.Start, k.Stop, k.Delete, k.Help, k.Quit}
}

// FullHelp is the expanded help view, laid out in columns.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Enter, k.Back},
		{k.New, k.Start, k.Stop, k.Restart},
		{k.Delete, k.Console, k.Command, k.Refresh},
		{k.Help, k.Quit},
	}
}
