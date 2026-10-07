package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// buildFoldHome returns a Home with group "work" holding parent "kata-orchestrator"
// with three children c1..c3, plus an unrelated childless session "solo".
func buildFoldHome(t *testing.T) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height = 160, 40
	h.initialLoading = false
	h.subSessionsFoldedDefault = false

	parent := session.NewInstanceWithGroup("kata-orchestrator", "/tmp/p", "work")
	parent.ID = "parent-id"
	insts := []*session.Instance{parent}
	for _, n := range []string{"c1", "c2", "c3"} {
		c := session.NewInstanceWithGroup(n, "/tmp/"+n, "work")
		c.ParentSessionID = parent.ID
		insts = append(insts, c)
	}
	solo := session.NewInstanceWithGroup("solo", "/tmp/solo", "work")
	insts = append(insts, solo)

	h.instancesMu.Lock()
	h.instances = insts
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTree(insts)
	h.rebuildFlatItems()
	return h
}

func foldIdx(h *Home, title string) int {
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.Title == title {
			return i
		}
	}
	return -1
}

func foldSubRows(h *Home) int {
	n := 0
	for _, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.IsSubSession {
			n++
		}
	}
	return n
}

func foldKey(t *testing.T, h *Home, msg tea.KeyMsg) {
	t.Helper()
	h.handleMainKey(msg)
}

func foldRenderRow(h *Home, idx int) string {
	var b strings.Builder
	h.renderSessionItem(&b, h.flatItems[idx], false, h.getSessionRenderSnapshot(), 160)
	return ansi.Strip(b.String())
}

func TestSubSessionFold_UnfoldedByDefault(t *testing.T) {
	h := buildFoldHome(t)
	if got := foldSubRows(h); got != 3 {
		t.Fatalf("sub-session rows = %d, want 3", got)
	}
	for _, n := range []string{"c1", "c2", "c3"} {
		if foldIdx(h, n) < 0 {
			t.Fatalf("child %s missing from flatItems", n)
		}
	}
}

func TestSubSessionFold_HOnParentFolds(t *testing.T) {
	for name, msg := range map[string]tea.KeyMsg{
		"h":    plainKeyMsg('h'),
		"left": {Type: tea.KeyLeft},
	} {
		t.Run(name, func(t *testing.T) {
			h := buildFoldHome(t)
			h.cursor = foldIdx(h, "kata-orchestrator")
			foldKey(t, h, msg)

			if got := foldSubRows(h); got != 0 {
				t.Fatalf("sub-session rows after fold = %d, want 0", got)
			}
			pi := foldIdx(h, "kata-orchestrator")
			if pi < 0 || h.cursor != pi {
				t.Fatalf("cursor = %d, want parent row %d", h.cursor, pi)
			}
			row := foldRenderRow(h, pi)
			if !strings.Contains(row, "▸") || !strings.Contains(row, "+3") {
				t.Fatalf("folded parent row missing \"▸\" or \"+3\": %q", row)
			}
		})
	}
}

func TestSubSessionFold_HOnChildFoldsParent(t *testing.T) {
	h := buildFoldHome(t)
	h.cursor = foldIdx(h, "c2")
	foldKey(t, h, plainKeyMsg('h'))

	if got := foldSubRows(h); got != 0 {
		t.Fatalf("sub-session rows = %d, want 0", got)
	}
	pi := foldIdx(h, "kata-orchestrator")
	if pi < 0 || h.cursor != pi {
		t.Fatalf("cursor = %d, want parent row %d", h.cursor, pi)
	}
}

func TestSubSessionFold_UnfoldKeys(t *testing.T) {
	for name, msg := range map[string]tea.KeyMsg{
		"l":     plainKeyMsg('l'),
		"right": {Type: tea.KeyRight},
		"tab":   {Type: tea.KeyTab},
	} {
		t.Run(name, func(t *testing.T) {
			h := buildFoldHome(t)
			h.cursor = foldIdx(h, "kata-orchestrator")
			foldKey(t, h, plainKeyMsg('h'))
			if foldSubRows(h) != 0 {
				t.Fatal("precondition: parent should be folded")
			}
			foldKey(t, h, msg)

			if got := foldSubRows(h); got != 3 {
				t.Fatalf("sub-session rows after unfold = %d, want 3", got)
			}
			pi := foldIdx(h, "kata-orchestrator")
			if pi < 0 || h.cursor != pi {
				t.Fatalf("cursor = %d, want parent row %d", h.cursor, pi)
			}
		})
	}
}

func TestSubSessionFold_HOnChildlessSessionCollapsesGroup(t *testing.T) {
	h := buildFoldHome(t)
	h.cursor = foldIdx(h, "solo")
	foldKey(t, h, plainKeyMsg('h'))

	if foldIdx(h, "solo") >= 0 {
		t.Fatal("childless session still visible: group should have collapsed")
	}
	if foldIdx(h, "kata-orchestrator") >= 0 {
		t.Fatal("group members still visible after h on childless session")
	}
	if h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].Type != session.ItemTypeGroup {
		t.Fatalf("cursor should land on the collapsed group header, cursor=%d", h.cursor)
	}
}

func TestSubSessionFold_DefaultFoldedAndExplicitUnfold(t *testing.T) {
	h := buildFoldHome(t)
	h.subSessionsFoldedDefault = true
	h.rebuildFlatItems()

	if got := foldSubRows(h); got != 0 {
		t.Fatalf("with default folded, sub-session rows = %d, want 0", got)
	}
	pi := foldIdx(h, "kata-orchestrator")
	if pi < 0 {
		t.Fatal("parent row missing")
	}
	row := foldRenderRow(h, pi)
	if !strings.Contains(row, "▸") || !strings.Contains(row, "+3") {
		t.Fatalf("default-folded parent row missing \"▸\" or \"+3\": %q", row)
	}

	h.cursor = pi
	foldKey(t, h, plainKeyMsg('l'))
	if got := foldSubRows(h); got != 3 {
		t.Fatalf("after explicit l, sub-session rows = %d, want 3", got)
	}
	h.rebuildFlatItems()
	if got := foldSubRows(h); got != 3 {
		t.Fatalf("explicit unfold did not survive rebuild: rows = %d, want 3", got)
	}
}
