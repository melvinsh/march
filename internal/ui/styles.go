package ui

import (
	"image/color"

	"charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
)

// Palette holds every colour the UI uses. Lip Gloss v2 is pure — it does not
// probe the terminal itself — so the background darkness is resolved once from
// the Bubble Tea program and the palette is built from it.
type Palette struct {
	Accent    color.Color
	Text      color.Color
	Muted     color.Color
	Subtle    color.Color
	Success   color.Color
	Warning   color.Color
	Danger    color.Color
	Border    color.Color
	Highlight color.Color
}

// NewPalette builds a palette for a light or dark terminal.
func NewPalette(isDark bool) Palette {
	ld := lipgloss.LightDark(isDark)
	return Palette{
		Accent:    ld(lipgloss.Color("#6C3FD8"), lipgloss.Color("#A78BFA")),
		Text:      ld(lipgloss.Color("#1F2430"), lipgloss.Color("#E6E6EB")),
		Muted:     ld(lipgloss.Color("#6B7280"), lipgloss.Color("#9CA3AF")),
		Subtle:    ld(lipgloss.Color("#9CA3AF"), lipgloss.Color("#6B7280")),
		Success:   ld(lipgloss.Color("#047857"), lipgloss.Color("#34D399")),
		Warning:   ld(lipgloss.Color("#B45309"), lipgloss.Color("#FBBF24")),
		Danger:    ld(lipgloss.Color("#B91C1C"), lipgloss.Color("#F87171")),
		Border:    ld(lipgloss.Color("#D1D5DB"), lipgloss.Color("#3F3F46")),
		Highlight: ld(lipgloss.Color("#EDE9FE"), lipgloss.Color("#2E1065")),
	}
}

// Styles is the rendered style set derived from a Palette.
type Styles struct {
	P Palette

	Title    lipgloss.Style
	Subtitle lipgloss.Style
	Body     lipgloss.Style
	Muted    lipgloss.Style
	Bold     lipgloss.Style

	Success lipgloss.Style
	Warning lipgloss.Style
	Danger  lipgloss.Style
	Accent  lipgloss.Style

	Panel      lipgloss.Style
	Row        lipgloss.Style
	RowFocused lipgloss.Style
	Header     lipgloss.Style
	StatusBar  lipgloss.Style
	Key        lipgloss.Style
	Badge      lipgloss.Style
}

// HelpStyles adapts the bubbles help component to this palette, which
// otherwise renders in its own hardcoded greys.
func (s Styles) HelpStyles() help.Styles {
	key := lipgloss.NewStyle().Foreground(s.P.Accent)
	desc := lipgloss.NewStyle().Foreground(s.P.Muted)
	sep := lipgloss.NewStyle().Foreground(s.P.Subtle)
	return help.Styles{
		Ellipsis:       sep,
		ShortKey:       key,
		ShortDesc:      desc,
		ShortSeparator: sep,
		FullKey:        key,
		FullDesc:       desc,
		FullSeparator:  sep,
	}
}

// NewStyles builds the style set for a terminal background.
func NewStyles(isDark bool) Styles {
	p := NewPalette(isDark)
	base := lipgloss.NewStyle().Foreground(p.Text)

	return Styles{
		P:        p,
		Title:    lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		Subtitle: lipgloss.NewStyle().Foreground(p.Muted),
		Body:     base,
		Muted:    lipgloss.NewStyle().Foreground(p.Muted),
		Bold:     base.Bold(true),

		Success: lipgloss.NewStyle().Foreground(p.Success),
		Warning: lipgloss.NewStyle().Foreground(p.Warning),
		Danger:  lipgloss.NewStyle().Foreground(p.Danger),
		Accent:  lipgloss.NewStyle().Foreground(p.Accent),

		Panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Border).
			Padding(0, 1),

		Row:        lipgloss.NewStyle().Padding(0, 1),
		RowFocused: lipgloss.NewStyle().Padding(0, 1).Background(p.Highlight).Bold(true),
		Header:     lipgloss.NewStyle().Foreground(p.Subtle).Padding(0, 1),
		StatusBar:  lipgloss.NewStyle().Foreground(p.Muted),
		Key:        lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		Badge: lipgloss.NewStyle().
			Foreground(p.Accent).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Border).
			Padding(0, 1),
	}
}
