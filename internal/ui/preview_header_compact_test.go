package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/x/ansi"
)

// TestPreviewHeader_CompactCombinesHeaderLine exercises the renderPreviewPane
// header contract: classic layout puts the session title/status on their own
// line and the "⏱ <activity>" marker on a later line, while compact folds
// the title, status, tool/group pills, and activity marker onto one line.
func TestPreviewHeader_CompactCombinesHeaderLine(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = false
	inst.Status = session.StatusRunning

	firstLine := func() string {
		t.Helper()
		rendered := ansi.Strip(home.renderPreviewPane(home.width, home.height))
		lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
		if len(lines) == 0 {
			t.Fatal("renderPreviewPane produced no output")
		}
		return lines[0]
	}

	home.compact = false
	classicFirst := firstLine()
	if !strings.Contains(classicFirst, inst.Title) {
		t.Fatalf("classic preview header's first line is missing the session title:\n%q", classicFirst)
	}
	if strings.Contains(classicFirst, "⏱") {
		t.Fatalf("classic preview header put the activity marker on the title line, want a later line:\n%q", classicFirst)
	}

	home.compact = true
	compactFirst := firstLine()
	if !strings.Contains(compactFirst, inst.Title) {
		t.Fatalf("compact preview header's first line is missing the session title:\n%q", compactFirst)
	}
	if !strings.Contains(compactFirst, "⏱") {
		t.Fatalf("compact preview header did not fold the activity marker onto the title line:\n%q", compactFirst)
	}
}
