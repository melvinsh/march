package ui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// pad right-pads a cell to a fixed width, measuring display width so styled or
// multi-byte content still lines up.
func pad(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	out := s
	for i := 0; i < width-w; i++ {
		out += " "
	}
	return out
}

// truncate shortens a string to n display columns, marking the cut.
func truncate(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	runes := []rune(s)
	if n <= 1 || len(runes) <= 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}

// formatMiB renders a memory size, preferring GiB once it divides evenly.
func formatMiB(mib int) string {
	if mib <= 0 {
		return "—"
	}
	if mib >= 1024 && mib%1024 == 0 {
		return fmt.Sprintf("%d GiB", mib/1024)
	}
	if mib >= 1024 {
		return fmt.Sprintf("%.1f GiB", float64(mib)/1024)
	}
	return fmt.Sprintf("%d MiB", mib)
}

// formatBytes renders a byte count in binary units.
func formatBytes(b int64) string {
	if b <= 0 {
		return "—"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGT"[exp])
}
