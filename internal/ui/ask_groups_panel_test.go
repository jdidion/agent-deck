package ui

// Spec: docs/design/2026-10-23-ask-groups.md, section "Asks panel".
// Every assertion names the spec bullet it checks.
//
// Seams these tests assume (the implementer adds them):
//   - (*AskPanel).OnHeader() bool
//   - (*AskPanel).CursorGroup() session.AskKind
//   - (*AskPanel).GroupItems(kind session.AskKind) []*statedb.AskItemRow
//   - (*AskPanel).Selected() returns nil while the cursor is on a header
//   - Home.askDenySink    func(inst *session.Instance) error  (replaces sending Escape)
//   - Home.askRestartSink func(inst *session.Instance) error  (replaces restartSession)
//   - Home.quickApproveSink (exists) receives the per-item and group approve.

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ---------- pure panel fixtures ----------

func askGroupsRow(id, kind, title string, age time.Duration) askRow {
	return askRow{
		item: &statedb.AskItemRow{
			ID:         id,
			InstanceID: "inst-" + id,
			Kind:       kind,
			Summary:    "summary " + id,
			CreatedAt:  time.Now().Add(-age),
		},
		title: title,
	}
}

func askGroupsPanel(rows ...askRow) *AskPanel {
	ap := NewAskPanel()
	ap.Show()
	ap.SetSize(140, 50)
	ap.SetRows(rows)
	return ap
}

func askGroupsPlain(s string) string { return tmux.StripANSI(s) }

// askGroupsTop moves the cursor to the first display row.
func askGroupsTop(ap *AskPanel) {
	for i := 0; i < 64; i++ {
		ap.MoveUp()
	}
}

// ---------- grouping, order, counts ----------

// Spec: "Rows are grouped under three headers, in this order: Needs approval,
// Needs decision, Error."
func TestAskGroups_HeadersInSpecOrder(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("e1", "error", "err-sess", time.Minute),
		askGroupsRow("d1", "decision", "dec-sess", time.Minute),
		askGroupsRow("a1", "approval", "appr-sess", time.Minute),
	)
	view := askGroupsPlain(ap.View())
	ia := strings.Index(view, "Needs approval")
	id := strings.Index(view, "Needs decision")
	ie := regexp.MustCompile(`Error[^\n0-9]{0,8}1`).FindStringIndex(view)
	if ia < 0 || id < 0 || ie == nil {
		t.Fatalf("missing a group header (approval=%d decision=%d error=%v):\n%s", ia, id, ie, view)
	}
	if !(ia < id && id < ie[0]) {
		t.Errorf("header order = approval@%d decision@%d error@%d, want approval < decision < error:\n%s", ia, id, ie[0], view)
	}
}

// Spec: "Each header shows its count."
func TestAskGroups_HeaderShowsCount(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("a1", "approval", "appr-1", time.Minute),
		askGroupsRow("a2", "approval", "appr-2", 2*time.Minute),
		askGroupsRow("a3", "approval", "appr-3", 3*time.Minute),
		askGroupsRow("d1", "decision", "dec-1", time.Minute),
	)
	view := askGroupsPlain(ap.View())
	if !regexp.MustCompile(`Needs approval[^\n0-9]{0,8}3`).MatchString(view) {
		t.Errorf("approval header should show count 3:\n%s", view)
	}
	if !regexp.MustCompile(`Needs decision[^\n0-9]{0,8}1`).MatchString(view) {
		t.Errorf("decision header should show count 1:\n%s", view)
	}
}

// Spec: "Empty groups are omitted."
func TestAskGroups_EmptyGroupOmitted(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("a1", "approval", "appr-1", time.Minute),
		askGroupsRow("e1", "error", "err-1", time.Minute),
	)
	view := askGroupsPlain(ap.View())
	if strings.Contains(view, "Needs decision") {
		t.Errorf("empty decision group must be omitted:\n%s", view)
	}
	if !strings.Contains(view, "Needs approval") {
		t.Errorf("non-empty approval group must render:\n%s", view)
	}
}

// Spec: "Within a group, newest first." Rows are supplied oldest first to prove
// the panel orders them, not the caller.
func TestAskGroups_NewestFirstWithinGroup(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("old", "approval", "oldest-appr", 30*time.Minute),
		askGroupsRow("mid", "approval", "middle-appr", 10*time.Minute),
		askGroupsRow("new", "approval", "newest-appr", time.Minute),
	)
	items := ap.GroupItems(session.AskApproval)
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "new,mid,old" {
		t.Errorf("GroupItems(approval) order = %v, want [new mid old]", ids)
	}
	view := askGroupsPlain(ap.View())
	in, im, io := strings.Index(view, "newest-appr"), strings.Index(view, "middle-appr"), strings.Index(view, "oldest-appr")
	if !(in >= 0 && in < im && im < io) {
		t.Errorf("rendered order newest@%d middle@%d oldest@%d, want newest first:\n%s", in, im, io, view)
	}
}

// Spec "Kinds": "Rows already stored with the old names are read as the new
// names." permission -> approval, question -> decision.
func TestAskGroups_OldStoredKindsReadAsNewNames(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("p1", "permission", "legacy-perm", time.Minute),
		askGroupsRow("q1", "question", "legacy-quest", time.Minute),
	)
	if got := ap.GroupItems(session.AskApproval); len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("GroupItems(approval) = %v, want the legacy permission row p1", got)
	}
	if got := ap.GroupItems(session.AskDecision); len(got) != 1 || got[0].ID != "q1" {
		t.Errorf("GroupItems(decision) = %v, want the legacy question row q1", got)
	}
	view := askGroupsPlain(ap.View())
	ia, ip := strings.Index(view, "Needs approval"), strings.Index(view, "legacy-perm")
	id, iq := strings.Index(view, "Needs decision"), strings.Index(view, "legacy-quest")
	if ia < 0 || id < 0 || !(ia < ip && ip < id && id < iq) {
		t.Errorf("legacy rows not under their new headers:\n%s", view)
	}
}

// ---------- cursor over headers ----------

type askCursorStop struct {
	header bool
	group  session.AskKind
	id     string // item id; "" on a header
}

func askCursorAt(ap *AskPanel) askCursorStop {
	s := askCursorStop{header: ap.OnHeader(), group: ap.CursorGroup()}
	if sel := ap.Selected(); sel != nil {
		s.id = sel.ID
	}
	return s
}

// Spec: "The cursor moves over item rows and header rows."
func TestAskGroups_CursorVisitsHeadersAndItems(t *testing.T) {
	ap := askGroupsPanel(
		askGroupsRow("e1", "error", "err-1", time.Minute),
		askGroupsRow("d1", "decision", "dec-1", time.Minute),
		askGroupsRow("a2", "approval", "appr-2", 5*time.Minute),
		askGroupsRow("a1", "approval", "appr-1", time.Minute),
	)
	want := []askCursorStop{
		{true, session.AskApproval, ""},
		{false, session.AskApproval, "a1"},
		{false, session.AskApproval, "a2"},
		{true, session.AskDecision, ""},
		{false, session.AskDecision, "d1"},
		{true, session.AskError, ""},
		{false, session.AskError, "e1"},
	}
	askGroupsTop(ap)
	for i, w := range want {
		if i > 0 {
			ap.MoveDown()
		}
		if got := askCursorAt(ap); got != w {
			t.Fatalf("stop %d = %+v, want %+v", i, got, w)
		}
	}
	// Clamp at the bottom.
	ap.MoveDown()
	if got := askCursorAt(ap); got != want[len(want)-1] {
		t.Errorf("cursor should clamp at the last row, got %+v", got)
	}
}

// Spec: header rows are selectable; on a header there is no single item, so
// Selected() is nil while OnHeader() is true.
func TestAskGroups_SelectedIsNilOnHeader(t *testing.T) {
	ap := askGroupsPanel(askGroupsRow("a1", "approval", "appr-1", time.Minute))
	askGroupsTop(ap)
	if !ap.OnHeader() {
		t.Fatal("top row should be the Needs approval header")
	}
	if sel := ap.Selected(); sel != nil {
		t.Errorf("Selected() on a header = %+v, want nil", sel)
	}
}

// ---------- Home fixture for key actions ----------

type askGroupsCapture struct {
	approved  []string
	approvedW []int
	denied    []string
	restarted []string
}

func (c *askGroupsCapture) approve(inst *session.Instance, windowIndex int) error {
	c.approved = append(c.approved, inst.ID)
	c.approvedW = append(c.approvedW, windowIndex)
	return nil
}
func (c *askGroupsCapture) deny(inst *session.Instance) error {
	c.denied = append(c.denied, inst.ID)
	return nil
}
func (c *askGroupsCapture) restart(inst *session.Instance) error {
	c.restarted = append(c.restarted, inst.ID)
	return nil
}
func (c *askGroupsCapture) total() int { return len(c.approved) + len(c.denied) + len(c.restarted) }

type askGroupsSeed struct {
	id     string // instance id == ask id suffix
	kind   string
	status session.Status
	age    time.Duration
}

// newAskGroupsHome builds a headless Home with a real sandboxed store holding
// one open ask per seed, the panel open, and every action sink captured.
func newAskGroupsHome(t *testing.T, seeds ...askGroupsSeed) (*Home, *statedb.StateDB, *askGroupsCapture) {
	t.Helper()
	setXDGTestHome(t)
	storage, err := session.NewStorageWithProfile("_test")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	db := storage.GetDB()
	if db == nil {
		t.Fatal("nil db")
	}

	insts := make([]*session.Instance, 0, len(seeds))
	now := time.Now()
	for _, s := range seeds {
		insts = append(insts, &session.Instance{ID: s.id, Title: "title-" + s.id, Tool: "claude", Status: s.status})
		if err := db.UpsertAskItem(&statedb.AskItemRow{
			ID:         "ask-" + s.id,
			InstanceID: s.id,
			Profile:    "_test",
			Kind:       s.kind,
			Summary:    "summary " + s.id,
			CreatedAt:  now.Add(-s.age),
		}); err != nil {
			t.Fatalf("seed ask %s: %v", s.id, err)
		}
	}

	h := NewGateHome(GateHomeOptions{Width: 140, Height: 50, Instances: insts}).home
	h.storage = storage
	h.askPanel = NewAskPanel()

	c := &askGroupsCapture{}
	h.quickApproveSink = c.approve
	h.askDenySink = c.deny
	h.askRestartSink = c.restart

	h.refreshAskPanel()
	h.askPanel.SetSize(h.width, h.height)
	h.askPanel.Show()
	return h, db, c
}

func askGroupsKey(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func askGroupsPress(h *Home, keys ...string) {
	for _, k := range keys {
		h.handleAskPanelKey(askGroupsKey(k))
	}
}

// askGroupsMoveToItem puts the cursor on the item whose ask id is id.
func askGroupsMoveToItem(t *testing.T, h *Home, id string) {
	t.Helper()
	askGroupsTop(h.askPanel)
	for i := 0; i < 64; i++ {
		if sel := h.askPanel.Selected(); sel != nil && sel.ID == id {
			return
		}
		h.askPanel.MoveDown()
	}
	t.Fatalf("item %s not reachable by cursor", id)
}

// askGroupsMoveToHeader puts the cursor on the header of group kind.
func askGroupsMoveToHeader(t *testing.T, h *Home, kind session.AskKind) {
	t.Helper()
	askGroupsTop(h.askPanel)
	for i := 0; i < 64; i++ {
		if h.askPanel.OnHeader() && h.askPanel.CursorGroup() == kind {
			return
		}
		h.askPanel.MoveDown()
	}
	t.Fatalf("header %s not reachable by cursor", kind)
}

// askGroupsPromptLine returns the rendered line carrying "(y/n)", or "".
func askGroupsPromptLine(h *Home) string {
	for _, line := range strings.Split(askGroupsPlain(h.View()), "\n") {
		if strings.Contains(line, "(y/n)") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func askGroupsOpenIDs(t *testing.T, db *statedb.StateDB) map[string]bool {
	t.Helper()
	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	m := make(map[string]bool, len(open))
	for _, it := range open {
		m[it.ID] = true
	}
	return m
}

// A standard mixed queue: two approvals, two decisions, two errors.
func askGroupsMixed() []askGroupsSeed {
	return []askGroupsSeed{
		{"ap1", "approval", session.StatusWaiting, 1 * time.Minute},
		{"ap2", "approval", session.StatusWaiting, 2 * time.Minute},
		{"de1", "decision", session.StatusWaiting, 1 * time.Minute},
		{"de2", "decision", session.StatusWaiting, 2 * time.Minute},
		{"er1", "error", session.StatusError, 1 * time.Minute},
		{"er2", "error", session.StatusError, 2 * time.Minute},
	}
}

// ---------- per-item keys ----------

// Spec: "on an approval item `y` approve (sends `1` + Enter, the same as
// quick-approve)" and "Per-item actions do not confirm."
func TestAskGroups_ItemY_ApprovesImmediatelyViaQuickApprove(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap2")
	askGroupsPress(h, "y")
	if len(c.approved) != 1 || c.approved[0] != "ap2" {
		t.Fatalf("approve dispatches = %v, want [ap2]", c.approved)
	}
	if c.approvedW[0] != -1 {
		t.Errorf("approve window index = %d, want -1 (session's active window, as quick-approve)", c.approvedW[0])
	}
	if p := askGroupsPromptLine(h); p != "" {
		t.Errorf("per-item approve must not confirm, got prompt %q", p)
	}
}

// Spec: "on an approval item ... `n` deny (sends Escape)", no confirmation.
func TestAskGroups_ItemN_DeniesImmediately(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askGroupsPress(h, "n")
	if len(c.denied) != 1 || c.denied[0] != "ap1" {
		t.Fatalf("deny dispatches = %v, want [ap1]", c.denied)
	}
	if len(c.approved) != 0 {
		t.Errorf("deny must not approve, got %v", c.approved)
	}
	if p := askGroupsPromptLine(h); p != "" {
		t.Errorf("per-item deny must not confirm, got prompt %q", p)
	}
}

// Spec: y/n apply only "on an approval item".
func TestAskGroups_ItemYN_NoOpOnDecisionAndErrorItems(t *testing.T) {
	for _, id := range []string{"ask-de1", "ask-er1"} {
		t.Run(id, func(t *testing.T) {
			h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
			askGroupsMoveToItem(t, h, id)
			askGroupsPress(h, "y")
			askGroupsPress(h, "n")
			if c.total() != 0 {
				t.Errorf("y/n on a non-approval item dispatched: %+v", c)
			}
		})
	}
}

// Spec: "`r` dismiss" (per item; "dismissed items resolve immediately, as
// today") and "After an action the panel refreshes from the store."
func TestAskGroups_ItemR_DismissesOnlyThatItem(t *testing.T) {
	h, db, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-de2")
	askGroupsPress(h, "r")
	open := askGroupsOpenIDs(t, db)
	if open["ask-de2"] {
		t.Error("dismissed item ask-de2 is still open in the store")
	}
	if len(open) != 5 {
		t.Errorf("open items = %d, want 5 (only one dismissed)", len(open))
	}
	if strings.Contains(askGroupsPlain(h.askPanel.View()), "title-de2") {
		t.Error("panel still shows the dismissed item; it must refresh from the store")
	}
	if p := askGroupsPromptLine(h); p != "" {
		t.Errorf("per-item dismiss must not confirm, got prompt %q", p)
	}
}

// Spec: "`enter` jump to the session".
func TestAskGroups_ItemEnter_JumpsToSession(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-er2")
	askGroupsPress(h, "enter")
	if h.askPanel.IsVisible() {
		t.Error("enter on an item should close the panel")
	}
	if h.cursor >= len(h.flatItems) || h.flatItems[h.cursor].Session == nil || h.flatItems[h.cursor].Session.ID != "er2" {
		t.Errorf("list cursor not on session er2 after jump")
	}
}

// ---------- group keys + confirmation ----------

// Spec: "`Y` approve all (approval group)" from an item row, and "Every group
// action first shows a confirmation naming the action and count, e.g.
// `Approve 15 pending tool calls? (y/n)`".
func TestAskGroups_GroupY_ShowsApproveConfirmationWithCount(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askGroupsPress(h, "Y")
	if p := askGroupsPromptLine(h); !strings.Contains(p, "Approve 2 pending tool calls? (y/n)") {
		t.Errorf("confirmation = %q, want it to contain %q", p, "Approve 2 pending tool calls? (y/n)")
	}
	if c.total() != 0 {
		t.Errorf("group action ran before confirmation: %+v", c)
	}
}

// Spec: "Only `y` proceeds" — approve-all reaches every approval item and no
// other group.
func TestAskGroups_GroupY_ConfirmedApprovesWholeGroup(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap2")
	askGroupsPress(h, "Y", "y")
	got := strings.Join(sortedCopy(c.approved), ",")
	if got != "ap1,ap2" {
		t.Errorf("approved = %v, want [ap1 ap2]", c.approved)
	}
	for _, w := range c.approvedW {
		if w != -1 {
			t.Errorf("group approve window index = %d, want -1", w)
		}
	}
	if len(c.denied)+len(c.restarted) != 0 {
		t.Errorf("approve-all dispatched other actions: %+v", c)
	}
	if p := askGroupsPromptLine(h); p != "" {
		t.Errorf("confirmation still shown after y: %q", p)
	}
}

// Spec: "Only `y` proceeds; any other key cancels."
func TestAskGroups_GroupConfirm_AnyOtherKeyCancels(t *testing.T) {
	for _, k := range []string{"n", "esc", "x", "Y", "enter"} {
		t.Run(k, func(t *testing.T) {
			h, db, c := newAskGroupsHome(t, askGroupsMixed()...)
			askGroupsMoveToItem(t, h, "ask-ap1")
			askGroupsPress(h, "Y", k)
			if c.total() != 0 {
				t.Errorf("key %q after the prompt proceeded: %+v", k, c)
			}
			if p := askGroupsPromptLine(h); p != "" {
				t.Errorf("key %q did not dismiss the prompt: %q", k, p)
			}
			if n := len(askGroupsOpenIDs(t, db)); n != 6 {
				t.Errorf("open items = %d after cancel, want 6", n)
			}
			if !h.askPanel.IsVisible() {
				t.Errorf("cancelling the prompt with %q closed the panel", k)
			}
		})
	}
}

// Spec: "On a header the per-item keys act on the whole group" — y on the
// approval header behaves as Y (confirm, then approve all).
func TestAskGroups_HeaderY_ActsOnWholeGroup(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToHeader(t, h, session.AskApproval)
	askGroupsPress(h, "y")
	if p := askGroupsPromptLine(h); !strings.Contains(p, "Approve 2 pending tool calls? (y/n)") {
		t.Fatalf("y on the approval header: prompt = %q, want the approve-all confirmation", p)
	}
	askGroupsPress(h, "y")
	if got := strings.Join(sortedCopy(c.approved), ","); got != "ap1,ap2" {
		t.Errorf("approved = %v, want [ap1 ap2]", c.approved)
	}
}

// Spec: "On a header the per-item keys act on the whole group" — r on a header
// dismisses the group (after confirmation).
func TestAskGroups_HeaderR_DismissesWholeGroup(t *testing.T) {
	h, db, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToHeader(t, h, session.AskDecision)
	askGroupsPress(h, "r")
	if p := askGroupsPromptLine(h); p == "" {
		t.Fatal("r on a header is a group action and must confirm first")
	}
	askGroupsPress(h, "y")
	open := askGroupsOpenIDs(t, db)
	if open["ask-de1"] || open["ask-de2"] {
		t.Errorf("decision items still open after header dismiss: %v", open)
	}
	if len(open) != 4 {
		t.Errorf("open = %d, want 4 (other groups untouched)", len(open))
	}
}

// Spec: "`N` deny all (approval group)", confirmed with a count.
func TestAskGroups_GroupN_ConfirmedDeniesWholeGroup(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askGroupsPress(h, "N")
	p := strings.ToLower(askGroupsPromptLine(h))
	if !strings.Contains(p, "deny") || !strings.Contains(p, "2") {
		t.Errorf("deny-all prompt = %q, want it to name the action (deny) and count (2)", p)
	}
	if c.total() != 0 {
		t.Fatalf("deny-all ran before confirmation: %+v", c)
	}
	askGroupsPress(h, "y")
	if got := strings.Join(sortedCopy(c.denied), ","); got != "ap1,ap2" {
		t.Errorf("denied = %v, want [ap1 ap2]", c.denied)
	}
	if len(c.approved) != 0 {
		t.Errorf("deny-all approved something: %v", c.approved)
	}
}

// Spec: "`R` restart all (error group)", confirmed with a count.
func TestAskGroups_GroupR_ConfirmedRestartsErrorGroup(t *testing.T) {
	h, _, c := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-er1")
	askGroupsPress(h, "R")
	p := strings.ToLower(askGroupsPromptLine(h))
	if !strings.Contains(p, "restart") || !strings.Contains(p, "2") {
		t.Errorf("restart-all prompt = %q, want it to name the action (restart) and count (2)", p)
	}
	if c.total() != 0 {
		t.Fatalf("restart-all ran before confirmation: %+v", c)
	}
	askGroupsPress(h, "y")
	if got := strings.Join(sortedCopy(c.restarted), ","); got != "er1,er2" {
		t.Errorf("restarted = %v, want [er1 er2]", c.restarted)
	}
}

// Spec: "`D` dismiss all (any group)", confirmed with a count, resolving only
// that group's items.
func TestAskGroups_GroupD_ConfirmedDismissesOnlyThatGroup(t *testing.T) {
	for _, tc := range []struct {
		cursorOn string
		gone     []string
	}{
		{"ask-ap1", []string{"ask-ap1", "ask-ap2"}},
		{"ask-de2", []string{"ask-de1", "ask-de2"}},
		{"ask-er1", []string{"ask-er1", "ask-er2"}},
	} {
		t.Run(tc.cursorOn, func(t *testing.T) {
			h, db, c := newAskGroupsHome(t, askGroupsMixed()...)
			askGroupsMoveToItem(t, h, tc.cursorOn)
			askGroupsPress(h, "D")
			p := strings.ToLower(askGroupsPromptLine(h))
			if !strings.Contains(p, "dismiss") || !strings.Contains(p, "2") {
				t.Errorf("dismiss-all prompt = %q, want it to name the action (dismiss) and count (2)", p)
			}
			if n := len(askGroupsOpenIDs(t, db)); n != 6 {
				t.Fatalf("dismiss-all ran before confirmation: open = %d", n)
			}
			askGroupsPress(h, "y")
			open := askGroupsOpenIDs(t, db)
			for _, id := range tc.gone {
				if open[id] {
					t.Errorf("%s still open after dismiss-all", id)
				}
			}
			if len(open) != 4 {
				t.Errorf("open = %d, want 4 (other groups untouched)", len(open))
			}
			if c.total() != 0 {
				t.Errorf("dismiss-all sent keys to sessions: %+v", c)
			}
			view := askGroupsPlain(h.askPanel.View())
			for _, id := range tc.gone {
				if strings.Contains(view, "title-"+strings.TrimPrefix(id, "ask-")) {
					t.Errorf("panel still shows dismissed %s; it must refresh from the store", id)
				}
			}
		})
	}
}

// Spec: Y/N are "(approval group)" and R is "(error group)": outside their
// group they do nothing, not even a prompt.
func TestAskGroups_GroupKeysOutsideTheirGroupDoNothing(t *testing.T) {
	for _, tc := range []struct{ cursorOn, key string }{
		{"ask-de1", "Y"},
		{"ask-er1", "Y"},
		{"ask-de1", "N"},
		{"ask-er1", "N"},
		{"ask-ap1", "R"},
		{"ask-de1", "R"},
	} {
		t.Run(tc.key+"@"+tc.cursorOn, func(t *testing.T) {
			h, db, c := newAskGroupsHome(t, askGroupsMixed()...)
			askGroupsMoveToItem(t, h, tc.cursorOn)
			askGroupsPress(h, tc.key)
			if p := askGroupsPromptLine(h); p != "" {
				t.Errorf("%s outside its group showed a prompt: %q", tc.key, p)
			}
			if c.total() != 0 {
				t.Errorf("%s outside its group dispatched: %+v", tc.key, c)
			}
			if n := len(askGroupsOpenIDs(t, db)); n != 6 {
				t.Errorf("open = %d, want 6", n)
			}
		})
	}
}

// Spec: "An approved, denied or restarted item resolves on the producer's next
// pass when its session leaves the attention status" — the panel must NOT
// resolve those rows itself.
func TestAskGroups_ApproveDenyRestartLeaveRowsForProducer(t *testing.T) {
	h, db, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askGroupsPress(h, "Y", "y")
	askGroupsMoveToItem(t, h, "ask-er1")
	askGroupsPress(h, "R", "y")
	if n := len(askGroupsOpenIDs(t, db)); n != 6 {
		t.Errorf("open = %d after approve-all and restart-all, want 6 (producer resolves them)", n)
	}
}

// ---------- footer hint ----------

func askGroupsHint(h *Home) string { return strings.ToLower(askGroupsPlain(h.askPanel.View())) }

// Spec: "Footer hint lists the keys that apply to the cursor's row."
func TestAskGroups_FooterHintOnApprovalItemListsApproveDeny(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-ap1")
	hint := askGroupsHint(h)
	for _, w := range []string{"approve", "deny", "dismiss"} {
		if !strings.Contains(hint, w) {
			t.Errorf("hint on an approval item should mention %q:\n%s", w, hint)
		}
	}
}

// Spec: "Footer hint lists the keys that apply to the cursor's row" — y/n do
// not apply to a decision item.
func TestAskGroups_FooterHintOnDecisionItemOmitsApprove(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToItem(t, h, "ask-de1")
	hint := askGroupsHint(h)
	if strings.Contains(hint, "approve") {
		t.Errorf("hint on a decision item must not offer approve:\n%s", hint)
	}
	if !strings.Contains(hint, "dismiss") {
		t.Errorf("hint on a decision item should offer dismiss:\n%s", hint)
	}
}

// Spec: "Footer hint lists the keys that apply to the cursor's row" — restart
// applies in the error group.
func TestAskGroups_FooterHintOnErrorHeaderListsRestart(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	askGroupsMoveToHeader(t, h, session.AskError)
	hint := askGroupsHint(h)
	if !strings.Contains(hint, "restart") {
		t.Errorf("hint on the error header should offer restart:\n%s", hint)
	}
	if strings.Contains(hint, "approve") {
		t.Errorf("hint on the error header must not offer approve:\n%s", hint)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
