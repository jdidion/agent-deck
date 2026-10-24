package session

// Spec: docs/design/2026-10-23-ask-groups.md, "Second review round
// (2026-10-24)", bullets "Daemon captures are bounded" (review findings f2,
// f10) and "Unknown is not "menu gone"" (f3, f17). Seams and helpers are
// listed at the top of ask_pane_truth_test.go (askCapture, askNow,
// askCaptureTTL, askTestClock, askCaptureFake).
//
// Budget seams assumed: statusProbeBudget bounds each ask capture and
// syncPassBudget bounds all of one syncAsks call's captures (both existing
// package vars). syncAsks measures the pass budget from its own start, and
// may read it from either the wall clock or askNow; the slow fakes below
// spend it on both.

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func setProbeBudgets(t *testing.T, perProbe, pass time.Duration) {
	t.Helper()
	prevProbe, prevPass := statusProbeBudget, syncPassBudget
	statusProbeBudget, syncPassBudget = perProbe, pass
	t.Cleanup(func() { statusProbeBudget, syncPassBudget = prevProbe, prevPass })
}

// waitingFleet returns n waiting instances whose hook record is a Bash
// PreToolUse, so a menu on screen makes each an approval.
func waitingFleet(n int) (map[string]*Instance, map[string]string, map[string]*HookStatus, []string) {
	byID := map[string]*Instance{}
	statuses := map[string]string{}
	hooks := map[string]*HookStatus{}
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("w%d", i)
		ids = append(ids, id)
		byID[id] = &Instance{ID: id}
		statuses[id] = "waiting"
		hooks[id] = &HookStatus{Event: "PreToolUse", ToolName: "Bash"}
	}
	return byID, statuses, hooks, ids
}

// ---------- only waiting instances ----------

// Spec "Daemon captures are bounded": "The producer captures only waiting
// instances" (f2: every pass spawned tmux for each session).
func TestSyncAsks_CapturesOnlyWaitingInstances(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	newAskClock(t)
	fake := installAskCaptureFake(t)
	statuses := map[string]string{"w": "waiting", "r": "running", "i": "idle", "e": "error", "s": "stopped"}
	byID := map[string]*Instance{}
	for id := range statuses {
		byID[id] = &Instance{ID: id}
		fake.setMenu(true, id)
	}
	d.syncAsks(profile, db, byID, statuses, nil)
	for id := range statuses {
		want := 0
		if id == "w" {
			want = 1
		}
		if got := fake.count(id); got != want {
			t.Errorf("%s (%s): %d captures, want %d", id, statuses[id], got, want)
		}
	}
}

// ---------- once per instance per TTL ----------

// Spec: "at most once per instance every 5 s". Passes 4 s apart reuse the
// capture; the pass 6 s after it captures again.
func TestSyncAsks_CapturesEachInstanceAtMostOncePerTTL(t *testing.T) {
	if askCaptureTTL != 5*time.Second {
		t.Fatalf("askCaptureTTL = %v, spec says 5s", askCaptureTTL)
	}
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	fake.setMenu(true, "a")
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := fake.count("a"); n != 1 {
		t.Fatalf("first pass: %d captures, want 1", n)
	}
	clk.Advance(4 * time.Second)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := fake.count("a"); n != 1 {
		t.Fatalf("pass 4s later: %d captures, want still 1 (within the 5s TTL)", n)
	}
	clk.Advance(2 * time.Second)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := fake.count("a"); n != 2 {
		t.Fatalf("pass 6s after the capture: %d captures, want 2", n)
	}
}

// Spec: "(reusing the last result in between)". The pane changes inside the
// TTL; the pass reads the cached capture, not the new pane.
func TestSyncAsks_ReusesLastCaptureWithinTTL(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID := map[string]*Instance{"a": {ID: "a"}}
	statuses := map[string]string{"a": "waiting"}
	hooks := map[string]*HookStatus{"a": {Event: "PreToolUse", ToolName: "Bash"}}

	fake.setMenu(false, "a")
	d.syncAsks(profile, db, byID, statuses, hooks)
	fake.setMenu(true, "a")
	clk.Advance(4 * time.Second)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if ids := openAskIDs(t, db); len(ids) != 0 {
		t.Fatalf("open rows = %v inside the TTL, want none (the cached no-menu capture is reused)", ids)
	}
	clk.Advance(2 * time.Second)
	d.syncAsks(profile, db, byID, statuses, hooks)
	if ids := openAskIDs(t, db); ids["a"] == "" {
		t.Fatalf("open rows = %v after the TTL, want an approval for a", ids)
	}
}

// Spec: resolve needs "two consecutive successful captures" without the menu;
// a reused cached result is not a second capture.
func TestSyncAsks_ReusedMissIsNotASecondMiss(t *testing.T) {
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
	fake.setMenu(false, "a")
	clk.Advance(pastTTL)
	d.syncAsks(profile, db, byID, statuses, hooks) // miss 1, captured
	clk.Advance(time.Second)
	d.syncAsks(profile, db, byID, statuses, hooks) // cached miss 1
	if got := openAskIDs(t, db)["a"]; got != first {
		t.Fatalf("open row = %q after one capture and one cached reuse, want %q kept", got, first)
	}
	clk.Advance(pastTTL)
	d.syncAsks(profile, db, byID, statuses, hooks) // miss 2, captured
	if got, ok := openAskIDs(t, db)["a"]; ok {
		t.Fatalf("row %q still open after the second captured miss, want it resolved", got)
	}
}

// ---------- per-capture budget ----------

// Spec: "each capture under the per-probe budget"; a capture that "timed out
// ... has an unknown substate: its open row is kept" (f10: the capture ran
// unbudgeted on the single-threaded poll loop).
func TestSyncAsks_CaptureOverProbeBudgetKeepsRowAndReturns(t *testing.T) {
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

	setProbeBudgets(t, 50*time.Millisecond, 30*time.Second)
	release, started, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enter, exit sync.Once
	fake.mu.Lock()
	fake.sub["a"] = SubstateNone
	fake.delay = func() {
		enter.Do(func() { close(started) })
		<-release
		exit.Do(func() { close(finished) })
	}
	fake.mu.Unlock()
	// Release the hung capture and let it return before askCapture is
	// restored (cleanups run last-registered first).
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-started:
			<-finished
		case <-time.After(2 * time.Second):
		}
	})

	clk.Advance(pastTTL)
	done := make(chan struct{})
	go func() {
		d.syncAsks(profile, db, byID, statuses, hooks)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		unblock()
		<-done
		t.Fatal("syncAsks blocked on a hung capture for 3s; statusProbeBudget is 50ms")
	}
	if got := openAskIDs(t, db)["a"]; got != first {
		t.Fatalf("open row = %q after a timed-out capture, want %q kept", got, first)
	}
}

// ---------- pass budget ----------

// slowCaptures makes every capture spend the pass budget on both the wall
// clock and askNow, while staying under the per-probe budget.
func slowCaptures(fake *askCaptureFake, clk *askTestClock, pass time.Duration) {
	fake.mu.Lock()
	fake.delay = func() {
		clk.Advance(pass + time.Millisecond)
		time.Sleep(2 * pass)
	}
	fake.mu.Unlock()
}

// Spec: the producer "stops capturing for the rest of the pass once the pass
// budget is spent".
func TestSyncAsks_StopsCapturingOncePassBudgetSpent(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID, statuses, hooks, ids := waitingFleet(4)
	fake.setMenu(true, ids...)
	setProbeBudgets(t, 6*time.Second, 20*time.Millisecond)
	slowCaptures(fake, clk, 20*time.Millisecond)

	d.syncAsks(profile, db, byID, statuses, hooks)
	if n := fake.total(); n != 1 {
		t.Fatalf("%d captures in a pass whose budget the first capture spent, want 1", n)
	}
}

// Spec "Unknown is not "menu gone"": an instance "skipped for budget has an
// unknown substate: ... no row is opened for it". Only the captured instance
// opens a row.
func TestSyncAsks_BudgetSkippedInstanceOpensNoRow(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID, statuses, hooks, ids := waitingFleet(4)
	fake.setMenu(true, ids...)
	setProbeBudgets(t, 6*time.Second, 20*time.Millisecond)
	slowCaptures(fake, clk, 20*time.Millisecond)

	d.syncAsks(profile, db, byID, statuses, hooks)
	if fake.total() >= len(ids) {
		t.Fatalf("precondition: all %d instances captured; the pass budget skipped none", fake.total())
	}
	open := openAskIDs(t, db)
	for _, id := range ids {
		if _, ok := open[id]; ok && fake.count(id) == 0 {
			t.Errorf("row opened for %s, which was never captured (skipped for budget)", id)
		}
	}
}

// Spec "Unknown is not "menu gone"": an instance skipped for budget keeps its
// open row. Every row first takes one captured miss, so the next miss
// resolves a captured instance; the budget-skipped ones must survive.
func TestSyncAsks_BudgetSkippedInstanceKeepsOpenRow(t *testing.T) {
	d, db, profile := newAskDaemon(t)
	clk := newAskClock(t)
	fake := installAskCaptureFake(t)
	byID, statuses, hooks, ids := waitingFleet(4)

	fake.setMenu(true, ids...)
	d.syncAsks(profile, db, byID, statuses, hooks)
	before := openAskIDs(t, db)
	if len(before) != 4 {
		t.Fatalf("setup: open rows = %v, want 4", before)
	}
	fake.setMenu(false, ids...)
	clk.Advance(pastTTL)
	d.syncAsks(profile, db, byID, statuses, hooks) // one captured miss each
	if n := len(openAskIDs(t, db)); n != 4 {
		t.Fatalf("setup: %d rows open after one miss, want 4", n)
	}

	setProbeBudgets(t, 6*time.Second, 20*time.Millisecond)
	slowCaptures(fake, clk, 20*time.Millisecond)
	countBefore := map[string]int{}
	for _, id := range ids {
		countBefore[id] = fake.count(id)
	}
	clk.Advance(pastTTL)
	d.syncAsks(profile, db, byID, statuses, hooks)

	after := openAskIDs(t, db)
	for _, id := range ids {
		if fake.count(id) > countBefore[id] {
			continue // captured: a second miss may resolve it
		}
		if after[id] != before[id] {
			t.Errorf("%s was skipped for budget but its row changed: %q -> %q", id, before[id], after[id])
		}
	}
	if skipped := 4 - (fake.total() - sumCounts(countBefore)); skipped < 3 {
		t.Errorf("only %d of 4 instances skipped; the pass budget should stop capturing after the first", skipped)
	}
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
