package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestPad(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  int
	}{
		{"abc", 10, 10},
		{"abc", 3, 3},
		{"abcdefghij", 5, 10}, // never truncates, only pads
		{"", 4, 4},
	}
	for _, tc := range tests {
		got := pad(tc.in, tc.width)
		if lipgloss.Width(got) != tc.want {
			t.Errorf("pad(%q, %d) has width %d, want %d", tc.in, tc.width, lipgloss.Width(got), tc.want)
		}
		if !strings.HasPrefix(got, tc.in) {
			t.Errorf("pad(%q, %d) = %q, which does not start with the input", tc.in, tc.width, got)
		}
	}
}

// Padding is what keeps the table columns aligned, so it must measure display
// width rather than bytes.
func TestPadMeasuresDisplayWidth(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Render("abc")
	got := pad(styled, 10)
	if lipgloss.Width(got) != 10 {
		t.Errorf("styled text padded to display width %d, want 10", lipgloss.Width(got))
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate did not leave a short string alone: %q", got)
	}
	got := truncate("a-very-long-machine-name", 10)
	if lipgloss.Width(got) > 10 {
		t.Errorf("truncate(%q, 10) = %q with width %d", "a-very-long-machine-name", got, lipgloss.Width(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate = %q, should mark the cut", got)
	}
	// Degenerate widths must not panic or produce nonsense.
	for _, n := range []int{0, 1, -1} {
		_ = truncate("abcdef", n)
	}
}

func TestFormatMiB(t *testing.T) {
	tests := map[int]string{
		0:     "—",
		512:   "512 MiB",
		1024:  "1 GiB",
		2048:  "2 GiB",
		8192:  "8 GiB",
		1536:  "1.5 GiB",
		12288: "12 GiB",
	}
	for in, want := range tests {
		if got := formatMiB(in); got != want {
			t.Errorf("formatMiB(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{
		0:              "—",
		512:            "512 B",
		1024:           "1.0 KiB",
		1536:           "1.5 KiB",
		1 << 20:        "1.0 MiB",
		470410 * 1024:  "459.4 MiB",
		1002614 * 1024: "979.1 MiB",
		int64(3) << 30: "3.0 GiB",
		int64(2) << 40: "2.0 TiB",
	}
	for in, want := range tests {
		if got := formatBytes(in); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStylesBuildForBothThemes(t *testing.T) {
	for _, dark := range []bool{true, false} {
		s := NewStyles(dark)
		// A style set with an unset foreground renders invisible text on one
		// of the two backgrounds.
		if s.Title.GetForeground() == nil {
			t.Errorf("dark=%v: the title style has no foreground", dark)
		}
		if s.Body.GetForeground() == nil {
			t.Errorf("dark=%v: the body style has no foreground", dark)
		}
		if got := s.Body.Render("x"); got == "" {
			t.Errorf("dark=%v: rendering produced nothing", dark)
		}
	}
}
