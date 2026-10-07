// Regression coverage for user report #2: "you were also supposed to put
// time, viewers and tags on the same line as session name and status." The
// compact preview header is supposed to fold the session title, status,
// tool/group pills, "⏱" activity marker, AND the viewers line onto a single
// line; classic keeps viewers off that first line. Modeled on
// TestPreviewHeader_CompactCombinesHeaderLine in preview_header_compact_test.go.
package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestPreviewHeader_CompactIncludesViewersOnFirstLine pins the contract: for a
// running (non-stopped) session, h.compact == true must fold the viewers text
// onto the same first line as the title and "⏱" activity marker, and must not
// repeat the viewers text on a later line.
func TestPreviewHeader_CompactIncludesViewersOnFirstLine(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	home.compact = true
	inst.Status = session.StatusRunning

	rendered := ansi.Strip(home.renderPreviewPane(home.width, home.height))
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("renderPreviewPane produced no output")
	}
	firstLine := lines[0]

	if !strings.Contains(firstLine, inst.Title) {
		t.Fatalf("compact preview header's first line is missing the session title:\n%q", firstLine)
	}
	if !strings.Contains(firstLine, "⏱") {
		t.Fatalf("compact preview header's first line is missing the activity marker:\n%q", firstLine)
	}
	// viewersLine renders one of "viewers unknown", "no viewers", or
	// "viewers: ...". "viewers" is the robust substring common to all three.
	if !strings.Contains(firstLine, "viewers") {
		t.Fatalf("compact preview header's first line is missing the viewers text:\n%q", firstLine)
	}

	for i, line := range lines[1:] {
		if strings.Contains(line, "viewers") {
			t.Fatalf("compact preview header repeated viewers text on a later line (index %d):\n%q", i+1, line)
		}
	}
}

// TestPreviewHeader_ClassicOmitsViewersFromFirstLine is the control case:
// h.compact == false must NOT put the viewers text on the first line (it
// belongs on its own later line in classic layout).
func TestPreviewHeader_ClassicOmitsViewersFromFirstLine(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	home.compact = false
	inst.Status = session.StatusRunning

	rendered := ansi.Strip(home.renderPreviewPane(home.width, home.height))
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("renderPreviewPane produced no output")
	}
	firstLine := lines[0]

	if strings.Contains(firstLine, "viewers") {
		t.Fatalf("classic preview header should not put viewers text on the first line:\n%q", firstLine)
	}
}
