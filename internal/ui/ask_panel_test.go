package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// mkAskRow builds a display row by hand, no DB needed.
func mkAskRow(id, instanceID, kind, summary, title string) askRow {
	return askRow{
		item: &statedb.AskItemRow{
			ID:         id,
			InstanceID: instanceID,
			Kind:       kind,
			Summary:    summary,
			CreatedAt:  time.Now().Add(-3 * time.Minute),
		},
		title: title,
	}
}

func TestAskPanelVisibility(t *testing.T) {
	ap := NewAskPanel()
	if ap.IsVisible() {
		t.Error("AskPanel should not be visible initially")
	}
	ap.Show()
	if !ap.IsVisible() {
		t.Error("AskPanel should be visible after Show()")
	}
	ap.Hide()
	if ap.IsVisible() {
		t.Error("AskPanel should not be visible after Hide()")
	}
}

func TestAskPanelEmpty(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(80, 24)

	if ap.Selected() != nil {
		t.Error("Selected() should be nil when the queue is empty")
	}

	view := ap.View()
	if !strings.Contains(view, "No open requests") {
		t.Errorf("empty-state view should mention the empty state, got:\n%s", view)
	}
}

func TestAskPanelSelectedTracksCursor(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(80, 24)
	ap.SetRows([]askRow{
		mkAskRow("a1", "i1", "permission", "run rm -rf", "flow"),
		mkAskRow("a2", "i2", "question", "which migration?", "natera"),
		mkAskRow("a3", "i3", "error", "build failed", "jdidion"),
	})

	// Display: [approval header, a1, decision header, a2, error header, a3].
	// The cursor starts on the first item, not its header.
	if sel := ap.Selected(); sel == nil || sel.ID != "a1" {
		t.Fatalf("initial Selected() should be a1, got %v", sel)
	}

	ap.MoveDown() // decision header
	if sel := ap.Selected(); sel != nil {
		t.Fatalf("on a header Selected() should be nil, got %v", sel)
	}
	ap.MoveDown()
	if sel := ap.Selected(); sel == nil || sel.ID != "a2" {
		t.Fatalf("after two MoveDown Selected() should be a2, got %v", sel)
	}

	ap.MoveDown() // error header
	ap.MoveDown()
	if sel := ap.Selected(); sel == nil || sel.ID != "a3" {
		t.Fatalf("after four MoveDown Selected() should be a3, got %v", sel)
	}

	// Clamp at the bottom.
	ap.MoveDown()
	if sel := ap.Selected(); sel == nil || sel.ID != "a3" {
		t.Fatalf("cursor should clamp at a3, got %v", sel)
	}

	ap.MoveUp() // error header
	ap.MoveUp()
	if sel := ap.Selected(); sel == nil || sel.ID != "a2" {
		t.Fatalf("after two MoveUp Selected() should be a2, got %v", sel)
	}

	// Clamp at the top (the approval header), one line above a1.
	for i := 0; i < 5; i++ {
		ap.MoveUp()
	}
	if !ap.OnHeader() || ap.Selected() != nil {
		t.Fatalf("cursor should clamp on the top header, got %v", ap.Selected())
	}
	ap.MoveDown()
	if sel := ap.Selected(); sel == nil || sel.ID != "a1" {
		t.Fatalf("one below the top header should be a1, got %v", sel)
	}
}

func TestAskPanelCursorClampsWhenRowsShrink(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetRows([]askRow{
		mkAskRow("a1", "i1", "permission", "s1", "t1"),
		mkAskRow("a2", "i2", "question", "s2", "t2"),
		mkAskRow("a3", "i3", "error", "s3", "t3"),
	})
	for i := 0; i < 4; i++ {
		ap.MoveDown() // past the decision and error headers to a3
	}
	if sel := ap.Selected(); sel == nil || sel.ID != "a3" {
		t.Fatalf("precondition: cursor should be on a3, got %v", sel)
	}

	// Queue shrinks below the cursor position.
	ap.SetRows([]askRow{
		mkAskRow("a1", "i1", "permission", "s1", "t1"),
	})
	if sel := ap.Selected(); sel == nil || sel.ID != "a1" {
		t.Fatalf("cursor should clamp to a1 after rows shrink, got %v", sel)
	}

	// And to nil when it empties entirely.
	ap.SetRows(nil)
	if ap.Selected() != nil {
		t.Error("Selected() should be nil after rows empty")
	}
}

func TestAskPanelViewRendersRows(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(80, 24)
	ap.SetRows([]askRow{
		mkAskRow("a1", "i1", "permission", "run a command", "flow-session"),
		mkAskRow("a2", "i2", "question", "which migration?", "natera-session"),
		mkAskRow("a3", "i3", "error", "build failed", "jdidion-session"),
	})

	view := ap.View()
	for _, title := range []string{"flow-session", "natera-session", "jdidion-session"} {
		if !strings.Contains(view, title) {
			t.Errorf("view should contain title %q, got:\n%s", title, view)
		}
	}
	// The error glyph (also used for session errors) should be present.
	if !strings.Contains(view, "✕") {
		t.Errorf("view should contain the error kind glyph, got:\n%s", view)
	}
}

func TestAskPanelViewDoesNotPanicAtSmallSizes(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetRows([]askRow{
		mkAskRow("a1", "i1", "permission", "a fairly long summary that should be truncated to fit", "a-long-session-title"),
	})

	for _, sz := range [][2]int{{80, 24}, {10, 3}, {1, 1}} {
		ap.SetSize(sz[0], sz[1])
		if got := ap.View(); got == "" {
			t.Errorf("View() at size %v should be non-empty", sz)
		}
	}
}

func TestAskPanelTitleFallsBackToInstanceID(t *testing.T) {
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(80, 24)
	ap.SetRows([]askRow{
		{item: &statedb.AskItemRow{ID: "a1", InstanceID: "inst-xyz", Kind: "question", CreatedAt: time.Now()}, title: "inst-xyz"},
	})
	if !strings.Contains(ap.View(), "inst-xyz") {
		t.Error("view should fall back to the InstanceID when no title is set")
	}
}
