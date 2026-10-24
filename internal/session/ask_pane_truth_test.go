package session

// Spec: docs/design/2026-10-23-ask-groups.md, sections "Kinds" (revised
// 2026-10-23), "Daemon autostart", "Other fixes from review" and "Second
// review round (2026-10-24)".
//
// Seams these tests assume (the implementer adds them):
//   - deriveAsk(hs *HookStatus, status string, substate Substate) (AskKind, string, bool)
//   - DeriveAskKind(hs *HookStatus, status string, substate Substate) AskKind
//   - askCapture func(inst *Instance) (Substate, error), package-level: the
//     raw pane capture syncAsks uses for a waiting instance. A non-nil error
//     means the substate is unknown. syncAsks reads every substate through it
//     (subject to the per-instance cache below). It replaces askSubstateOf,
//     which these tests no longer use; the implementer may remove it.
//   - askNow func() time.Time, package-level, default time.Now: the clock the
//     capture cache reads.
//   - askCaptureTTL time.Duration = 5 * time.Second: a waiting instance is
//     captured at most once per TTL; in between, its last result is reused.
//     The per-instance cache (and the consecutive-miss count) lives on
//     TransitionDaemon, so a fresh daemon starts empty.
//   - syncAsks keeps its signature. Its captures run under statusProbeBudget
//     each and stop for the rest of the pass once syncPassBudget is spent,
//     measured from the start of the syncAsks call (ask_capture_bounds_test.go).
//   - TransitionDaemon.Run returns nil once the HOME it started with is
//     missing or is no longer the same directory (os.SameFile), checked before
//     the priming pass and after every pass (no seam; driven by temp HOMEs).
//
// The capture fakes may be called from goroutines (a capture runs under a
// budget), so they are goroutine-safe.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// setAskCapture overrides askCapture for one test.
func setAskCapture(t *testing.T, fn func(*Instance) (Substate, error)) {
	t.Helper()
	prev := askCapture
	askCapture = fn
	t.Cleanup(func() { askCapture = prev })
}

// setAskSubstate overrides askCapture with a capture that always succeeds and
// reports fn's substate. Tests whose substate changes between passes must also
// advance an askTestClock past askCaptureTTL, or the cached result is reused.
func setAskSubstate(t *testing.T, fn func(*Instance) Substate) {
	t.Helper()
	setAskCapture(t, func(inst *Instance) (Substate, error) { return fn(inst), nil })
}

// askTestClock is a manual clock installed as askNow.
type askTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAskClock(t *testing.T) *askTestClock {
	t.Helper()
	c := &askTestClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	prev := askNow
	askNow = c.Now
	t.Cleanup(func() { askNow = prev })
	return c
}

func (c *askTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *askTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// pastTTL is a clock step that always expires a cached capture.
const pastTTL = askCaptureTTL + time.Second

// askCaptureFake is a scripted askCapture: per instance a substate (default
// SubstateNone) or an error, and a call count.
type askCaptureFake struct {
	mu    sync.Mutex
	sub   map[string]Substate
	err   map[string]error
	calls map[string]int
	// delay, when set, runs before every capture returns (budget tests).
	delay func()
}

func installAskCaptureFake(t *testing.T) *askCaptureFake {
	t.Helper()
	f := &askCaptureFake{sub: map[string]Substate{}, err: map[string]error{}, calls: map[string]int{}}
	setAskCapture(t, f.capture)
	return f
}

func (f *askCaptureFake) capture(inst *Instance) (Substate, error) {
	f.mu.Lock()
	f.calls[inst.ID]++
	sub, err, delay := f.sub[inst.ID], f.err[inst.ID], f.delay
	f.mu.Unlock()
	if delay != nil {
		delay()
	}
	return sub, err
}

func (f *askCaptureFake) setMenu(open bool, ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		delete(f.err, id)
		if open {
			f.sub[id] = SubstateInteractiveMenu
		} else {
			f.sub[id] = SubstateNone
		}
	}
}

func (f *askCaptureFake) setErr(err error, ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range ids {
		f.err[id] = err
	}
}

func (f *askCaptureFake) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

func (f *askCaptureFake) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += c
	}
	return n
}

// openAskIDs maps instance id to its open ask row id.
func openAskIDs(t *testing.T, db *statedb.StateDB) map[string]string {
	t.Helper()
	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	m := make(map[string]string, len(open))
	for _, it := range open {
		m[it.InstanceID] = it.ID
	}
	return m
}

var errAskCaptureFailed = errors.New("tmux capture-pane: timeout")

// askMenuFor returns a substate function that reports an open menu for the
// listed instance ids and SubstateNone for every other instance.
func askMenuFor(ids ...string) func(*Instance) Substate {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(inst *Instance) Substate {
		if inst != nil && set[inst.ID] {
			return SubstateInteractiveMenu
		}
		return SubstateNone
	}
}

func askMenuAll(*Instance) Substate { return SubstateInteractiveMenu }

// ---------- deriveAsk: revised Kinds table ----------

// Spec "Kinds" (revised). One subtest per table condition; each case names
// the row it checks.
func TestDeriveAsk_RevisedKindsTable(t *testing.T) {
	menu, none, idle := SubstateInteractiveMenu, SubstateNone, SubstateIdleAtEmptyPrompt
	tests := []struct {
		name    string
		hs      *HookStatus
		status  string
		sub     Substate
		wantOK  bool
		wantK   AskKind
		wantSum string // "" = do not check
	}{
		// Live check (spec "Kinds" revision note): 19 of 21 waiting+PreToolUse
		// sessions were at their normal prompt. Without the menu: not an ask.
		{"REGRESSION waiting PreToolUse Bash, no menu", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", none, false, "", ""},
		{"waiting PreToolUse Bash at empty prompt", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", idle, false, "", ""},
		// "Hook events alone never make an approval or decision."
		{"waiting PermissionRequest, no menu", &HookStatus{Event: "PermissionRequest"}, "waiting", none, false, "", ""},
		{"waiting permission_prompt matcher, no menu", &HookStatus{Event: "Notification", Matcher: "permission_prompt"}, "waiting", none, false, "", ""},
		{"waiting elicitation_dialog, no menu", &HookStatus{Event: "Notification", Matcher: "elicitation_dialog"}, "waiting", none, false, "", ""},
		{"waiting AskUserQuestion, no menu", &HookStatus{Event: "PreToolUse", ToolName: "AskUserQuestion"}, "waiting", none, false, "", ""},
		{"waiting no hook, no menu", nil, "waiting", none, false, "", ""},

		// approval row: waiting + interactive-menu + not a decision.
		{"menu + PreToolUse Bash is approval", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", menu, true, AskApproval, "wants to run Bash"},
		{"menu + PermissionRequest is approval", &HookStatus{Event: "PermissionRequest"}, "waiting", menu, true, AskApproval, "wants permission to run a command"},
		{"menu + permission_prompt prefers message", &HookStatus{Event: "Notification", Matcher: "permission_prompt", Message: "Run npm test?"}, "waiting", menu, true, AskApproval, "Run npm test?"},
		{"menu + no hook record is approval", nil, "waiting", menu, true, AskApproval, "wants permission to run a command"},
		{"menu + stale Stop record is approval", &HookStatus{Event: "Stop"}, "waiting", menu, true, AskApproval, "wants permission to run a command"},
		// "`ExitPlanMode` (plan approval) is an `approval`."
		{"menu + ExitPlanMode is approval", &HookStatus{Event: "PreToolUse", ToolName: "ExitPlanMode"}, "waiting", menu, true, AskApproval, "wants to run ExitPlanMode"},

		// decision row: menu + matcher elicitation_dialog, or tool AskUserQuestion (any event).
		{"menu + elicitation_dialog is decision", &HookStatus{Event: "Notification", Matcher: "elicitation_dialog"}, "waiting", menu, true, AskDecision, "is asking a question"},
		{"menu + elicitation_dialog prefers message", &HookStatus{Event: "Notification", Matcher: "elicitation_dialog", Message: "Which env?"}, "waiting", menu, true, AskDecision, "Which env?"},
		{"menu + PreToolUse AskUserQuestion is decision", &HookStatus{Event: "PreToolUse", ToolName: "AskUserQuestion"}, "waiting", menu, true, AskDecision, ""},
		{"menu + AskUserQuestion under Notification is decision", &HookStatus{Event: "Notification", ToolName: "AskUserQuestion"}, "waiting", menu, true, AskDecision, ""},
		{"menu + AskUserQuestion under PermissionRequest is decision", &HookStatus{Event: "PermissionRequest", ToolName: "AskUserQuestion"}, "waiting", menu, true, AskDecision, ""},

		// "Comparisons are case-insensitive."
		{"case: ELICITATION_DIALOG", &HookStatus{Event: "notification", Matcher: "ELICITATION_DIALOG"}, "waiting", menu, true, AskDecision, ""},
		{"case: askuserquestion", &HookStatus{Event: "pretooluse", ToolName: "askuserquestion"}, "waiting", menu, true, AskDecision, ""},

		// error row: status error, unchanged.
		{"error, no menu", nil, "error", none, true, AskError, "hit an error"},
		{"error with menu and permission hook stays error", &HookStatus{Event: "PermissionRequest"}, "error", menu, true, AskError, ""},
		{"error with message", &HookStatus{Message: "push rejected"}, "error", none, true, AskError, "push rejected"},

		// "(not an ask)": every other status, even with a menu.
		{"running with menu is not an ask", &HookStatus{Event: "PermissionRequest"}, "running", menu, false, "", ""},
		{"idle with menu is not an ask", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "idle", menu, false, "", ""},
		{"stopped with menu is not an ask", nil, "stopped", menu, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, sum, ok := deriveAsk(tt.hs, tt.status, tt.sub)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v (kind %q), want %v", ok, k, tt.wantOK)
			}
			if !ok {
				return
			}
			if k != tt.wantK {
				t.Errorf("kind = %q, want %q", k, tt.wantK)
			}
			if tt.wantSum != "" && sum != tt.wantSum {
				t.Errorf("summary = %q, want %q", sum, tt.wantSum)
			}
		})
	}
}

// Spec "Kinds": the row glyphs use "the same classification as the ask
// queue", so DeriveAskKind must produce the Kinds table's kind. Expected kinds
// come from the table, not from deriveAsk (review finding f11).
func TestDeriveAskKind_FollowsKindsTable(t *testing.T) {
	cases := []struct {
		name   string
		hs     *HookStatus
		status string
		sub    Substate
		want   AskKind
	}{
		{"waiting without menu is not an ask", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", SubstateNone, ""},
		{"waiting with menu is approval", &HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", SubstateInteractiveMenu, AskApproval},
		{"AskUserQuestion with menu is decision", &HookStatus{Event: "PreToolUse", ToolName: "AskUserQuestion"}, "waiting", SubstateInteractiveMenu, AskDecision},
		{"error status is error", nil, "error", SubstateNone, AskError},
		{"waiting at empty prompt is not an ask", &HookStatus{Event: "Stop"}, "waiting", SubstateIdleAtEmptyPrompt, ""},
	}
	for _, c := range cases {
		if got := DeriveAskKind(c.hs, c.status, c.sub); got != c.want {
			t.Errorf("%s: DeriveAskKind = %q, want %q", c.name, got, c.want)
		}
	}
}

// ---------- syncAsks through the substate seam ----------

// Live check (spec "Kinds" revision): 21 sessions waiting with last hook
// PreToolUse, only 2 showing a permission dialog. Exactly those 2 open.
func TestSyncAsks_LiveCheckOnlyMenuSessionsOpenApprovals(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{}
	statuses := map[string]string{}
	hooks := map[string]*HookStatus{}
	for i := 0; i < 21; i++ {
		id := fmt.Sprintf("s%02d", i)
		byID[id] = &Instance{ID: id}
		statuses[id] = "waiting"
		hooks[id] = &HookStatus{Event: "PreToolUse", ToolName: "Bash"}
	}
	setAskSubstate(t, askMenuFor("s03", "s17"))

	d.syncAsks(profile, db, byID, statuses, hooks)

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	got := map[string]string{}
	for _, it := range open {
		got[it.InstanceID] = it.Kind
	}
	if len(got) != 2 || got["s03"] != "approval" || got["s17"] != "approval" {
		t.Fatalf("open asks = %v, want approvals for exactly s03 and s17", got)
	}
}

// Spec "Second review round", bullet "Unknown is not "menu gone"": "An open
// approval or decision resolves on a missing menu only after two consecutive
// successful captures without one" (review findings f3, f17). One subtest
// per stored kind; both run the same miss sequence.
func TestSyncAsks_MenuGoneResolvesOnlyAfterTwoConsecutiveMisses(t *testing.T) {
	for _, c := range []struct{ kind, tool string }{
		{"approval", "Bash"},
		{"decision", "AskUserQuestion"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			d, db, profile := newAskDaemon(t)
			clk := newAskClock(t)
			fake := installAskCaptureFake(t)
			byID := map[string]*Instance{"a": {ID: "a"}}
			statuses := map[string]string{"a": "waiting"}
			hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: c.tool}}

			fake.setMenu(true, "a")
			d.syncAsks(profile, db, byID, statuses, hooks)
			first := openAskIDs(t, db)["a"]
			if first == "" {
				t.Fatal("setup: no row opened while the menu was on screen")
			}

			fake.setMenu(false, "a") // menu gone; hook record and status unchanged
			clk.Advance(pastTTL)
			d.syncAsks(profile, db, byID, statuses, hooks)
			if got := openAskIDs(t, db)["a"]; got != first {
				t.Fatalf("after one miss open row = %q, want the original %q kept", got, first)
			}

			clk.Advance(pastTTL)
			d.syncAsks(profile, db, byID, statuses, hooks)
			if got, ok := openAskIDs(t, db)["a"]; ok {
				t.Fatalf("after two consecutive misses row %q still open, want it resolved", got)
			}
		})
	}
}

// Spec "Unknown is not "menu gone"" (finding f3: "Add a test where the menu
// disappears for one pass and returns and assert the row ID is unchanged").
func TestSyncAsks_MenuReturningAfterOneMissKeepsRowID(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	fake.setMenu(true, "a")
	d.syncAsks(profile, db, byID, statuses, hooks)
	first := openAskIDs(t, db)["a"]
	if first == "" {
		t.Fatal("setup: no row opened")
	}
	for _, open := range []bool{false, true} {
		fake.setMenu(open, "a")
		clk.Advance(pastTTL)
		d.syncAsks(profile, db, byID, statuses, hooks)
	}
	if got := openAskIDs(t, db)["a"]; got != first {
		t.Fatalf("open row = %q after miss-then-menu, want the original %q", got, first)
	}
}

// Spec: the two misses must be "consecutive". A miss, a sighting, then one
// more miss is one miss in a row, so the row stays.
func TestSyncAsks_MissCountResetsWhenMenuReturns(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	fake.setMenu(true, "a")
	d.syncAsks(profile, db, byID, statuses, hooks)
	first := openAskIDs(t, db)["a"]
	for _, open := range []bool{false, true, false} {
		fake.setMenu(open, "a")
		clk.Advance(pastTTL)
		d.syncAsks(profile, db, byID, statuses, hooks)
	}
	if got := openAskIDs(t, db)["a"]; first == "" || got != first {
		t.Fatalf("open row = %q after miss, menu, miss; want the original %q kept", got, first)
	}
}

// Spec "Unknown is not "menu gone"": "An instance whose capture errored, timed
// out or was skipped for budget has an unknown substate: its open row is kept"
// (f17: "On error, keep the open row"). Several failing passes in a row, so
// errors are not counted as misses either.
func TestSyncAsks_CaptureErrorKeepsOpenRow(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	fake.setMenu(true, "a")
	d.syncAsks(profile, db, byID, statuses, hooks)
	first := openAskIDs(t, db)["a"]
	if first == "" {
		t.Fatal("setup: no row opened")
	}
	fake.setErr(errAskCaptureFailed, "a")
	for i := 0; i < 3; i++ {
		clk.Advance(pastTTL)
		d.syncAsks(profile, db, byID, statuses, hooks)
	}
	if got := openAskIDs(t, db)["a"]; got != first {
		t.Fatalf("open row = %q after three failed captures, want the original %q kept", got, first)
	}
}

// Spec "Unknown is not "menu gone"": for an unknown substate "no row is opened
// for it", whatever the hook record says.
func TestSyncAsks_CaptureErrorOpensNoRow(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	newAskClock(t)
	fake := installAskCaptureFake(t)
	fake.setErr(errAskCaptureFailed, "a")
	d.syncAsks(profile, db, map[string]*Instance{"a": {ID: "a"}},
		map[string]string{"a": "waiting"},
		map[string]*HookStatus{"a": {Event: "PermissionRequest", ToolName: "Bash"}})
	if ids := openAskIDs(t, db); len(ids) != 0 {
		t.Fatalf("open rows = %v after a failed capture, want none", ids)
	}
}

// Contract: a row stays open while the instance still derives the same kind
// (sustained menu), as before: one row, same id.
func TestSyncAsks_SustainedMenuKeepsSingleRow(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}
	setAskSubstate(t, askMenuAll)

	d.syncAsks(profile, db, byID, statuses, hooks)
	open, _ := db.ListOpenAskItems()
	if len(open) != 1 {
		t.Fatalf("setup: open = %d, want 1", len(open))
	}
	first := open[0].ID
	for i := 0; i < 3; i++ {
		d.syncAsks(profile, db, byID, statuses, hooks)
	}
	open, _ = db.ListOpenAskItems()
	if len(open) != 1 || open[0].ID != first {
		t.Fatalf("sustained menu: open = %+v, want the single original row %s", open, first)
	}
}

// Contract: "no longer derives an ask of that kind" covers a kind change. A
// stored approval whose instance now derives a decision is resolved, and the
// open phase (which runs after resolve) opens a decision.
func TestSyncAsks_KindChangeResolvesAndReopensAsNewKind(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	setAskSubstate(t, askMenuAll)

	d.syncAsks(profile, db, byID, statuses, map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}})
	d.syncAsks(profile, db, byID, statuses, map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "AskUserQuestion"}})

	open, err := db.ListOpenAskItems()
	if err != nil {
		t.Fatalf("ListOpenAskItems: %v", err)
	}
	if len(open) != 1 || open[0].Kind != "decision" {
		t.Fatalf("open = %+v, want one decision row", open)
	}
}

// Spec "Kinds": "Rows already stored with the old names are read as the new
// names." A legacy "permission" row whose instance still shows the menu is
// still an approval and must not be resolved as a kind change.
func TestSyncAsks_LegacyPermissionRowKeptWhileMenuOpen(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	row := &statedb.AskItemRow{
		ID: askItemID("a", "legacy"), InstanceID: "a", Profile: profile,
		Kind: "permission", Summary: "legacy", CreatedAt: time.Now(),
	}
	if err := db.UpsertAskItem(row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	setAskSubstate(t, askMenuAll)
	d.syncAsks(profile, db, map[string]*Instance{"a": {ID: "a"}},
		map[string]string{"a": "waiting"},
		map[string]*HookStatus{"a": {Event: "PermissionRequest"}})

	open, _ := db.ListOpenAskItems()
	if len(open) != 1 || open[0].ID != row.ID {
		t.Fatalf("open = %+v, want the legacy row %s kept", open, row.ID)
	}
}

// Contract: "open error rows keep today's behavior": an error row stays open
// while the status is error, whatever the substate.
func TestSyncAsks_ErrorRowStaysOpenWithoutMenu(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	setAskSubstate(t, askMenuFor())
	byID := map[string]*Instance{"e": {ID: "e"}}
	for i := 0; i < 3; i++ {
		d.syncAsks(profile, db, byID, map[string]string{"e": "error"}, nil)
	}
	open, _ := db.ListOpenAskItems()
	if len(open) != 1 || open[0].Kind != "error" {
		t.Fatalf("open = %+v, want one error row kept open", open)
	}
}

// Contract 3: syncAsks must get the substate through the askCapture seam (not
// a direct CachedSubstate call) for every waiting instance.
func TestSyncAsks_ReadsSubstateThroughSeam(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	fake := installAskCaptureFake(t)
	fake.setMenu(true, "a", "b")
	byID := map[string]*Instance{"a": {ID: "a"}, "b": {ID: "b"}}
	d.syncAsks(profile, db, byID, map[string]string{"a": "waiting", "b": "waiting"},
		map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}, "b": {Event: "PreToolUse", ToolName: "Bash"}})
	if fake.count("a") == 0 || fake.count("b") == 0 {
		t.Fatalf("askCapture calls a=%d b=%d, want both captured", fake.count("a"), fake.count("b"))
	}
	if n := openAskCount(t, d, profile); n != 2 {
		t.Fatalf("open = %d, want 2 (seam reported a menu for both)", n)
	}
}

// ---------- Instance.AskKind uses the cached substate ----------

// Spec "Kinds" (live check) + contract 4: (*Instance).AskKind uses
// CachedSubstate(). An instance with no tmux pane has SubstateNone, so a
// waiting session whose last hook is PreToolUse(Bash) is not an ask.
func TestInstanceAskKind_StalePreToolUseWithoutMenuIsNotAnAsk(t *testing.T) {
	inst := &Instance{ID: "row", Tool: "claude", Status: StatusWaiting}
	inst.hookEvent, inst.hookToolName = "PreToolUse", "Bash"
	if sub := inst.CachedSubstate(); sub == SubstateInteractiveMenu {
		t.Fatalf("precondition: cached substate = %q, want no menu", sub)
	}
	if got := inst.AskKind(StatusWaiting); got != "" {
		t.Fatalf("AskKind = %q, want \"\" (waiting without the menu is your turn)", got)
	}
}

// Error is unchanged: no menu needed.
func TestInstanceAskKind_ErrorWithoutMenuIsError(t *testing.T) {
	inst := &Instance{ID: "row", Tool: "claude", Status: StatusError}
	if got := inst.AskKind(StatusError); got != AskError {
		t.Fatalf("AskKind = %q, want %q", got, AskError)
	}
}

// ---------- hook evidence clearing ----------

// Spec "Other fixes from review": "Clearing a session's hook evidence
// (`ClearHookStatus`, ...) also clears the cached matcher and tool name, so a
// stale record cannot keep a row on `?`."
func TestClearHookStatus_ClearsMatcherAndToolName(t *testing.T) {
	inboxTestHome(t)
	inst := &Instance{ID: "clear-hook-evidence", Tool: "claude"}
	inst.hookStatus = "waiting"
	inst.hookEvent = "Notification"
	inst.hookMatcher = "permission_prompt"
	inst.hookToolName = "Bash"
	inst.hookLastUpdate = time.Now()

	inst.ClearHookStatus()

	inst.mu.RLock()
	matcher, tool := inst.hookMatcher, inst.hookToolName
	inst.mu.RUnlock()
	if matcher != "" || tool != "" {
		t.Fatalf("after ClearHookStatus matcher=%q tool=%q, want both empty", matcher, tool)
	}
}

// Spec "Other fixes from review": the Hermes spawn seed also clears the
// cached matcher and tool name.
func TestHermesSpawnSeed_ClearsMatcherAndToolName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("hermes-seed-clear", t.TempDir(), "hermes")
	inst.mu.Lock()
	inst.hookEvent = "PreToolUse"
	inst.hookMatcher = "permission_prompt"
	inst.hookToolName = "Bash"
	inst.mu.Unlock()

	if _, err := inst.seedHermesHookGeneration("starting", false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	inst.mu.RLock()
	matcher, tool := inst.hookMatcher, inst.hookToolName
	inst.mu.RUnlock()
	if matcher != "" || tool != "" {
		t.Fatalf("after the Hermes spawn seed matcher=%q tool=%q, want both empty", matcher, tool)
	}
}

// ---------- notify daemon exits when its HOME is gone ----------

// startDaemonWithHome runs TransitionDaemon.Run under HOME=home, which need
// not exist. The temp HOME holds no profiles, so passes write nothing under it.
func startDaemonWithHome(t *testing.T, home string) <-chan error {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	ctx, cancelFn := context.WithTimeout(context.Background(), 20*time.Second)
	ch := make(chan error, 1)
	exited := make(chan struct{})
	d := NewTransitionDaemon()
	go func() {
		ch <- d.Run(ctx)
		close(exited)
	}()
	t.Cleanup(func() {
		cancelFn()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	return ch
}

func runDaemonUnderHome(t *testing.T) (home string, done <-chan error) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home, startDaemonWithHome(t, home)
}

func requireDaemonExit(t *testing.T, done <-chan error, within time.Duration, why string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean nil exit", err)
		}
	case <-time.After(within):
		t.Fatalf("daemon still running %v after %s", within, why)
	}
}

// Spec "Daemon autostart": "the notify daemon exits cleanly on its next poll
// once the HOME it started with no longer exists." No sleep before the
// removal: per the second review round HOME is checked before the priming
// pass and after every pass, so the daemon must exit wherever the removal
// lands.
func TestTransitionDaemonRun_ExitsWhenHomeRemoved(t *testing.T) {
	home, done := runDaemonUnderHome(t)
	// Run may be creating its hooks dir under HOME at this moment, which can
	// make one RemoveAll fail with "directory not empty"; retry briefly.
	var err error
	for i := 0; i < 20; i++ {
		if err = os.RemoveAll(home); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("remove HOME: %v", err)
	}
	requireDaemonExit(t, done, 10*time.Second, "its HOME was removed (poll is at most notifyPollSlow)")
}

// Spec "Second review round", bullet "HOME identity": the daemon "exits when
// HOME is missing or is no longer the same directory (os.SameFile) ... so a
// pass that recreates HOME cannot keep it alive" (findings f1, f18).
//
// The old HOME is renamed away, not deleted, so the new directory at the same
// path cannot reuse its inode and the os.SameFile check is deterministic. The
// sleep only lets Run record HOME's identity (its first statements) before the
// swap; it does not race a pass, since nothing here writes under HOME.
func TestTransitionDaemonRun_ExitsWhenHomeReplacedAtSamePath(t *testing.T) {
	home, done := runDaemonUnderHome(t)
	time.Sleep(300 * time.Millisecond)
	if err := os.Rename(home, home+".old"); err != nil {
		t.Fatalf("move HOME away: %v", err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("recreate HOME: %v", err)
	}
	requireDaemonExit(t, done, 10*time.Second, "its HOME was replaced by a new directory at the same path")
}

// Spec "HOME identity": checked "before the first pass" (finding f18: the
// priming SyncOnce ran with no check). A daemon whose HOME is already gone
// exits at once instead of after the first poll interval (notifyPollSlow with
// no profiles).
func TestTransitionDaemonRun_ExitsBeforePrimingPassWhenHomeMissing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "never-created")
	done := startDaemonWithHome(t, home)
	requireDaemonExit(t, done, notifyPollFast, "starting with a missing HOME (it must not wait for a poll)")
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("HOME %s exists after the daemon exited (stat err %v); nothing may recreate it", home, err)
	}
}

// Guard for the same rule: the daemon must not exit while its HOME exists.
func TestTransitionDaemonRun_KeepsRunningWhileHomeExists(t *testing.T) {
	_, done := runDaemonUnderHome(t)
	select {
	case err := <-done:
		t.Fatalf("daemon exited (%v) with its HOME still present", err)
	case <-time.After(notifyPollSlow + time.Second):
	}
}
