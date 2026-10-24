package ui

// Spec: docs/design/2026-10-23-ask-groups.md "Other fixes from review": "The
// Asks panel scrolls: it renders only the rows that fit its height, keeps the
// cursor in view, and always shows the footer and any confirmation prompt."

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const askScrollRows = 40

func askScrollPanel(height int) *AskPanel {
	rows := make([]askRow, 0, askScrollRows)
	for i := 0; i < askScrollRows; i++ {
		// Newest first within the group, so row-00 is the top item.
		rows = append(rows, askGroupsRow(fmt.Sprintf("id%02d", i), "approval",
			fmt.Sprintf("row-%02d", i), time.Duration(i+1)*time.Minute))
	}
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(140, height)
	ap.SetRows(rows)
	return ap
}

func askVisibleRows(view string) int {
	n := 0
	for i := 0; i < askScrollRows; i++ {
		if strings.Contains(view, fmt.Sprintf("row-%02d", i)) {
			n++
		}
	}
	return n
}

// "renders only the rows that fit its height": the view is no taller than
// the panel and does not list every row.
func TestAskPanelScroll_RendersOnlyRowsThatFit(t *testing.T) {
	ap := askScrollPanel(16)
	view := askGroupsPlain(ap.View())
	if h := len(strings.Split(strings.TrimRight(view, "\n"), "\n")); h > 16 {
		t.Errorf("view is %d lines tall, want <= 16", h)
	}
	if n := askVisibleRows(view); n == 0 || n >= askScrollRows {
		t.Errorf("view shows %d of %d rows, want some but not all:\n%s", n, askScrollRows, view)
	}
}

// "always shows the footer".
func TestAskPanelScroll_FooterHintAlwaysVisible(t *testing.T) {
	ap := askScrollPanel(16)
	if view := askGroupsPlain(ap.View()); !strings.Contains(view, "↑/↓ move") {
		t.Errorf("footer hint missing from a scrolled panel:\n%s", view)
	}
	for i := 0; i < askScrollRows; i++ {
		ap.MoveDown()
	}
	if view := askGroupsPlain(ap.View()); !strings.Contains(view, "↑/↓ move") {
		t.Errorf("footer hint missing with the cursor at the bottom:\n%s", view)
	}
}

// "and any confirmation prompt".
func TestAskPanelScroll_ConfirmPromptAlwaysVisible(t *testing.T) {
	ap := askScrollPanel(16)
	ap.SetConfirm("Approve 40 pending tool calls? (y/n)")
	if view := askGroupsPlain(ap.View()); !strings.Contains(view, "Approve 40 pending tool calls? (y/n)") {
		t.Errorf("confirmation prompt missing from a scrolled panel:\n%s", view)
	}
}

// "keeps the cursor in view": the cursor line is visible on first fill, after
// moving to the last item, and after moving back to the top.
func TestAskPanelScroll_CursorStaysInView(t *testing.T) {
	ap := askScrollPanel(16)
	if view := askGroupsPlain(ap.View()); !strings.Contains(view, "row-00") {
		t.Fatalf("first item (the cursor) not visible on open:\n%s", view)
	}
	for i := 0; i < askScrollRows; i++ {
		ap.MoveDown()
	}
	if sel := ap.Selected(); sel == nil || sel.ID != fmt.Sprintf("id%02d", askScrollRows-1) {
		t.Fatalf("precondition: cursor on %+v, want the last item", sel)
	}
	view := askGroupsPlain(ap.View())
	if !strings.Contains(view, fmt.Sprintf("row-%02d", askScrollRows-1)) {
		t.Errorf("last item not scrolled into view with the cursor on it:\n%s", view)
	}
	if strings.Contains(view, "row-00") {
		t.Errorf("view still shows the top item while the cursor is on the last of %d:\n%s", askScrollRows, view)
	}
	askGroupsTop(ap)
	if view := askGroupsPlain(ap.View()); !strings.Contains(view, "Needs approval") {
		t.Errorf("group header (cursor at top) not scrolled back into view:\n%s", view)
	}
}

// Guard: a panel tall enough for every row still shows them all.
func TestAskPanelScroll_TallPanelShowsAllRows(t *testing.T) {
	ap := askScrollPanel(80)
	if n := askVisibleRows(askGroupsPlain(ap.View())); n != askScrollRows {
		t.Errorf("tall panel shows %d of %d rows", n, askScrollRows)
	}
}
