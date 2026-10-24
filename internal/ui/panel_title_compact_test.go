package ui

import (
	"strings"
	"testing"
)

// TestRenderPanelTitle_CompactIsSingleLine exercises the renderPanelTitle
// contract: classic layout renders the title on its own line followed by a
// full-width underline (two lines), while compact folds the underline onto
// the title's own line as a trailing rule (one line, no newline).
func TestRenderPanelTitle_CompactIsSingleLine(t *testing.T) {
	const width = 40
	const title = "SESSIONS"

	h := &Home{compact: false}
	classic := h.renderPanelTitle(title, width)
	if got := strings.Count(classic, "\n"); got != 1 {
		t.Fatalf("classic renderPanelTitle has %d newlines, want 1 (two lines):\n%q", got, classic)
	}

	h.compact = true
	compact := h.renderPanelTitle(title, width)
	if got := strings.Count(compact, "\n"); got != 0 {
		t.Fatalf("compact renderPanelTitle has %d newlines, want 0 (one line):\n%q", got, compact)
	}
	if !strings.Contains(compact, title) {
		t.Fatalf("compact renderPanelTitle dropped the title text:\n%q", compact)
	}
	if !strings.Contains(compact, "─") {
		t.Fatalf("compact renderPanelTitle is missing the trailing rule:\n%q", compact)
	}
}
