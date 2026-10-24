package session

// Spec: docs/design/2026-10-23-ask-groups.md, sections "Kinds" (revised
// 2026-10-23), "Daemon autostart" and "Other fixes from review".
//
// Seams these tests assume (the implementer adds them):
//   - deriveAsk(hs *HookStatus, status string, substate Substate) (AskKind, string, bool)
//   - DeriveAskKind(hs *HookStatus, status string, substate Substate) AskKind
//   - askSubstateOf func(*Instance) Substate, package-level, default
//     (*Instance).CachedSubstate; syncAsks reads every instance's substate
//     through it.
//   - TransitionDaemon.Run returns nil on its next poll once the HOME it
//     started with no longer exists (no seam; driven by removing a temp HOME).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// setAskSubstate overrides askSubstateOf for one test.
func setAskSubstate(t *testing.T, fn func(*Instance) Substate) {
	t.Helper()
	prev := askSubstateOf
	askSubstateOf = fn
	t.Cleanup(func() { askSubstateOf = prev })
}

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

// Spec "Kinds": DeriveAskKind is the kind-only form; the row glyphs use "the
// same classification as the ask queue".
func TestDeriveAskKind_MatchesDeriveAsk(t *testing.T) {
	cases := []struct {
		hs     *HookStatus
		status string
		sub    Substate
	}{
		{&HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", SubstateNone},
		{&HookStatus{Event: "PreToolUse", ToolName: "Bash"}, "waiting", SubstateInteractiveMenu},
		{&HookStatus{Event: "PreToolUse", ToolName: "AskUserQuestion"}, "waiting", SubstateInteractiveMenu},
		{nil, "error", SubstateNone},
		{&HookStatus{Event: "Stop"}, "waiting", SubstateIdleAtEmptyPrompt},
	}
	for i, c := range cases {
		want, _, _ := deriveAsk(c.hs, c.status, c.sub)
		if got := DeriveAskKind(c.hs, c.status, c.sub); got != want {
			t.Errorf("case %d: DeriveAskKind = %q, deriveAsk kind = %q", i, got, want)
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

// Spec "Kinds": an approval requires the menu. Contract: an OPEN approval row
// whose instance no longer derives an approval (menu gone, status still
// waiting) is resolved on the next pass.
func TestSyncAsks_ResolvesApprovalWhenMenuGoneWhileWaiting(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	setAskSubstate(t, askMenuAll)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := openAskCount(t, d, profile); n != 1 {
		t.Fatalf("setup: open = %d, want 1", n)
	}

	setAskSubstate(t, askMenuFor()) // menu closed; hook record and status unchanged
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d after the menu closed, want 0 (stale PreToolUse record must not hold the row)", n)
	}
}

// Same contract for a decision row.
func TestSyncAsks_ResolvesDecisionWhenMenuGoneWhileWaiting(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	byID := map[string]*Instance{"q": {ID: "q"}}
	statuses := map[string]string{"q": "waiting"}
	hooks := map[string]*HookStatus{"q": {Event: "PreToolUse", ToolName: "AskUserQuestion"}}

	setAskSubstate(t, askMenuAll)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := openAskCount(t, d, profile); n != 1 {
		t.Fatalf("setup: open = %d, want 1", n)
	}
	setAskSubstate(t, askMenuFor())
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := openAskCount(t, d, profile); n != 0 {
		t.Fatalf("open = %d after the picker closed, want 0", n)
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

// Contract 3: syncAsks must get the substate through askSubstateOf (not a
// direct CachedSubstate call), once per live instance at least.
func TestSyncAsks_ReadsSubstateThroughSeam(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	seen := map[string]bool{}
	setAskSubstate(t, func(inst *Instance) Substate {
		seen[inst.ID] = true
		return SubstateInteractiveMenu
	})
	byID := map[string]*Instance{"a": {ID: "a"}, "b": {ID: "b"}}
	d.syncAsks(profile, db, byID, map[string]string{"a": "waiting", "b": "waiting"},
		map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}, "b": {Event: "PreToolUse", ToolName: "Bash"}})
	if !seen["a"] || !seen["b"] {
		t.Fatalf("askSubstateOf saw %v, want both a and b", seen)
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

func runDaemonUnderHome(t *testing.T) (home string, done <-chan error, cancel context.CancelFunc) {
	t.Helper()
	home = filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	ctx, cancelFn := context.WithTimeout(context.Background(), 20*time.Second)
	ch := make(chan error, 1)
	d := NewTransitionDaemon()
	go func() { ch <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancelFn()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
		}
	})
	return home, ch, cancelFn
}

// Spec "Daemon autostart": "the notify daemon exits cleanly on its next poll
// once the HOME it started with no longer exists."
func TestTransitionDaemonRun_ExitsWhenHomeRemoved(t *testing.T) {
	home, done, _ := runDaemonUnderHome(t)
	time.Sleep(500 * time.Millisecond) // let the first pass start
	if err := os.RemoveAll(home); err != nil {
		t.Fatalf("remove HOME: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want a clean nil exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon still running 10s after its HOME was removed (poll is at most notifyPollSlow)")
	}
}

// Guard for the same rule: the daemon must not exit while its HOME exists.
func TestTransitionDaemonRun_KeepsRunningWhileHomeExists(t *testing.T) {
	_, done, _ := runDaemonUnderHome(t)
	select {
	case err := <-done:
		t.Fatalf("daemon exited (%v) with its HOME still present", err)
	case <-time.After(notifyPollSlow + time.Second):
	}
}
