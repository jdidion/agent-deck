package ui

// Spec: docs/design/2026-10-23-ask-groups.md, sections "Kinds" (revised) and
// "Dispatching keys". The driving report: approve-all typed a stray "1" into
// sessions sitting at their normal prompt (live check: 19 of 21
// waiting+PreToolUse sessions showed no dialog).
//
// Seams these tests assume (package-level vars the implementer adds):
//   - askPaneHasMenu func(inst *session.Instance) bool
//       fresh pane capture + tmux.HasOpenInteractiveMenu.
//   - askSendApprove func(inst *session.Instance) error   sends "1"+Enter.
//   - askSendDeny    func(inst *session.Instance) error   sends Escape.
//   - uiCachedSubstate func(inst *session.Instance) session.Substate
//       default (*session.Instance).CachedSubstate. The TUI's ask
//       classification (render-snapshot askKind for row glyphs and filter-bar
//       counts, and the approve/deny confirmation count) must use
//       session.DeriveAskKind(<hook record>, status, uiCachedSubstate(inst)).
//       This seam is NOT in the original contract list; it is the only way a
//       ui test can put an instance into the interactive-menu substate
//       without a live tmux pane.
//   - askDispatchResultMsg{Action string; Sent, Skipped, Failed int;
//       FailedTitles []string}, returned by the dispatch tea.Cmd.
//   - Home.askRestartSink is kept (restart is not part of this change).
//
// The result line is read from h.err (setError) or, failing that, from the
// rendered View; either satisfies "reports one result message".

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// ---------- seam installers ----------

// askSeamRecorder records every call into the dispatch seams. Sends may run
// on several goroutines ("sessions in parallel"), so it is locked.
type askSeamRecorder struct {
	mu         sync.Mutex
	approved   []string
	denied     []string
	paneChecks []string
	hasMenu    func(id string) bool
	failSend   map[string]error
}

func (r *askSeamRecorder) snapshot() (approved, denied, checks []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.approved...), append([]string(nil), r.denied...), append([]string(nil), r.paneChecks...)
}

func (r *askSeamRecorder) sends() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.approved) + len(r.denied)
}

// installAskDispatchSeams replaces askPaneHasMenu/askSendApprove/askSendDeny
// for one test. hasMenu nil means every pane shows the menu.
func installAskDispatchSeams(t *testing.T, hasMenu func(id string) bool) *askSeamRecorder {
	t.Helper()
	r := &askSeamRecorder{hasMenu: hasMenu, failSend: map[string]error{}}
	prevMenu, prevApprove, prevDeny := askPaneHasMenu, askSendApprove, askSendDeny
	askPaneHasMenu = func(inst *session.Instance) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.paneChecks = append(r.paneChecks, inst.ID)
		if r.hasMenu == nil {
			return true
		}
		return r.hasMenu(inst.ID)
	}
	askSendApprove = func(inst *session.Instance) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.failSend[inst.ID]; err != nil {
			return err
		}
		r.approved = append(r.approved, inst.ID)
		return nil
	}
	askSendDeny = func(inst *session.Instance) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.failSend[inst.ID]; err != nil {
			return err
		}
		r.denied = append(r.denied, inst.ID)
		return nil
	}
	t.Cleanup(func() { askPaneHasMenu, askSendApprove, askSendDeny = prevMenu, prevApprove, prevDeny })
	return r
}

// setUICachedSubstate overrides the TUI's cached-substate read for one test.
func setUICachedSubstate(t *testing.T, fn func(inst *session.Instance) session.Substate) {
	t.Helper()
	prev := uiCachedSubstate
	uiCachedSubstate = fn
	t.Cleanup(func() { uiCachedSubstate = prev })
}

func uiMenuFor(ids ...string) func(*session.Instance) session.Substate {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(inst *session.Instance) session.Substate {
		if inst != nil && set[inst.ID] {
			return session.SubstateInteractiveMenu
		}
		return session.SubstateNone
	}
}

func idSet(ids ...string) func(string) bool {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(id string) bool { return set[id] }
}

// ---------- cmd plumbing ----------

// askKeyCmd delivers one key to the panel and returns the cmd, without
// running it.
func askKeyCmd(h *Home, k string) tea.Cmd {
	_, cmd := h.handleAskPanelKey(askGroupsKey(k))
	return cmd
}

// askRunCmd runs cmd (walking tea.Batch) and returns every
// askDispatchResultMsg it yields.
func askRunCmd(cmd tea.Cmd) []askDispatchResultMsg {
	if cmd == nil {
		return nil
	}
	var out []askDispatchResultMsg
	var walk func(tea.Msg)
	walk = func(msg tea.Msg) {
		switch m := msg.(type) {
		case tea.BatchMsg:
			for _, c := range m {
				if c != nil {
					walk(c())
				}
			}
		case askDispatchResultMsg:
			out = append(out, m)
		}
	}
	walk(cmd())
	return out
}

// askOneResult runs cmd and requires exactly one result message.
func askOneResult(t *testing.T, cmd tea.Cmd) askDispatchResultMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("approve/deny returned a nil tea.Cmd; dispatch must run off the event loop")
	}
	res := askRunCmd(cmd)
	if len(res) != 1 {
		t.Fatalf("dispatch yielded %d askDispatchResultMsg, want exactly 1", len(res))
	}
	return res[0]
}

// askStatusText is where the result line can appear: the footer error/status
// (h.err) or the rendered screen.
func askStatusText(h *Home) string {
	var parts []string
	if h.err != nil {
		parts = append(parts, h.err.Error())
	}
	parts = append(parts, askGroupsPlain(h.View()))
	return strings.Join(parts, "\n")
}

// askMixHome: five approval rows; their cached substate says menu (the cache
// lags), but only ap1 and ap2 show the menu on a fresh capture.
func askMixHome(t *testing.T) (*Home, *askSeamRecorder) {
	t.Helper()
	h, _, _ := newAskGroupsHome(t,
		askGroupsSeed{"ap1", "approval", session.StatusWaiting, 1 * time.Minute},
		askGroupsSeed{"ap2", "approval", session.StatusWaiting, 2 * time.Minute},
		askGroupsSeed{"ap3", "approval", session.StatusWaiting, 3 * time.Minute},
		askGroupsSeed{"ap4", "approval", session.StatusWaiting, 4 * time.Minute},
		askGroupsSeed{"ap5", "approval", session.StatusWaiting, 5 * time.Minute},
	)
	r := installAskDispatchSeams(t, idSet("ap1", "ap2"))
	return h, r
}

// ---------- the regression ----------

// Spec "Dispatching keys": approve is "sent only to a session whose pane shows
// the open menu at the moment of sending"; "A session that fails the check is
// skipped and counted, never sent to." Driving report: stray "1" typed into
// sessions at their prompt.
func TestAskDispatch_ApproveAllSendsOnlyToPanesWithMenu(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap1")
	if cmd := askKeyCmd(h, "Y"); cmd != nil {
		askRunCmd(cmd) // the prompt itself must not dispatch
	}
	res := askOneResult(t, askKeyCmd(h, "y"))

	approved, denied, _ := r.snapshot()
	if got := strings.Join(sortedCopy(approved), ","); got != "ap1,ap2" {
		t.Fatalf("approved = %v, want exactly [ap1 ap2] (ap3..ap5 show no prompt)", approved)
	}
	if len(denied) != 0 {
		t.Errorf("approve-all denied %v", denied)
	}
	if res.Action != "approve" || res.Sent != 2 || res.Skipped != 3 || res.Failed != 0 {
		t.Errorf("result = %+v, want Action=approve Sent=2 Skipped=3 Failed=0", res)
	}
}

// Spec "Dispatching keys": deny-all follows the same check.
func TestAskDispatch_DenyAllSendsOnlyToPanesWithMenu(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap3")
	askRunCmd(askKeyCmd(h, "N"))
	res := askOneResult(t, askKeyCmd(h, "y"))

	approved, denied, _ := r.snapshot()
	if got := strings.Join(sortedCopy(denied), ","); got != "ap1,ap2" {
		t.Fatalf("denied = %v, want exactly [ap1 ap2]", denied)
	}
	if len(approved) != 0 {
		t.Errorf("deny-all approved %v", approved)
	}
	if res.Action != "deny" || res.Sent != 2 || res.Skipped != 3 || res.Failed != 0 {
		t.Errorf("result = %+v, want Action=deny Sent=2 Skipped=3 Failed=0", res)
	}
}

// Spec "Dispatching keys": "dispatch takes a fresh pane capture" for every
// targeted session, at sending time.
func TestAskDispatch_ChecksEveryTargetPane(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askRunCmd(askKeyCmd(h, "Y"))
	askOneResult(t, askKeyCmd(h, "y"))
	_, _, checks := r.snapshot()
	if got := strings.Join(sortedCopy(checks), ","); got != "ap1,ap2,ap3,ap4,ap5" {
		t.Errorf("pane checks = %v, want one per approval row", checks)
	}
}

// Spec "Dispatching keys": "The per-item `y`/`n` follow the same check."
func TestAskDispatch_ItemYSkipsPaneWithoutMenu(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap4")
	res := askOneResult(t, askKeyCmd(h, "y"))
	if n := r.sends(); n != 0 {
		t.Fatalf("per-item y sent %d keys to a session with no prompt on screen", n)
	}
	if res.Sent != 0 || res.Skipped != 1 {
		t.Errorf("result = %+v, want Sent=0 Skipped=1", res)
	}
}

func TestAskDispatch_ItemYApprovesPaneWithMenu(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap2")
	res := askOneResult(t, askKeyCmd(h, "y"))
	approved, _, _ := r.snapshot()
	if len(approved) != 1 || approved[0] != "ap2" {
		t.Fatalf("approved = %v, want [ap2]", approved)
	}
	if res.Action != "approve" || res.Sent != 1 || res.Skipped != 0 {
		t.Errorf("result = %+v, want approve Sent=1 Skipped=0", res)
	}
}

func TestAskDispatch_ItemNDeniesPaneWithMenuAndSkipsOther(t *testing.T) {
	t.Run("menu", func(t *testing.T) {
		h, r := askMixHome(t)
		askGroupsMoveToItem(t, h, "ask-ap1")
		res := askOneResult(t, askKeyCmd(h, "n"))
		_, denied, _ := r.snapshot()
		if len(denied) != 1 || denied[0] != "ap1" || res.Action != "deny" || res.Sent != 1 {
			t.Fatalf("denied = %v result = %+v, want [ap1], deny Sent=1", denied, res)
		}
	})
	t.Run("no menu", func(t *testing.T) {
		h, r := askMixHome(t)
		askGroupsMoveToItem(t, h, "ask-ap5")
		res := askOneResult(t, askKeyCmd(h, "n"))
		if r.sends() != 0 || res.Skipped != 1 {
			t.Fatalf("sends = %d result = %+v, want no send and Skipped=1", r.sends(), res)
		}
	})
}

// ---------- off the event loop ----------

// Spec "Dispatching keys": "Dispatch runs off the Bubble Tea event loop (a
// `tea.Cmd`)". The key handler itself must neither capture panes nor send.
func TestAskDispatch_KeyHandlerDoesNotSendSynchronously(t *testing.T) {
	cases := []struct {
		name string
		at   string
		keys []string
	}{
		{"group Y", "ask-ap1", []string{"Y", "y"}},
		{"group N", "ask-ap1", []string{"N", "y"}},
		{"item y", "ask-ap1", []string{"y"}},
		{"item n", "ask-ap2", []string{"n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, r := askMixHome(t)
			askGroupsMoveToItem(t, h, c.at)
			var last tea.Cmd
			for _, k := range c.keys {
				last = askKeyCmd(h, k)
			}
			if _, _, checks := r.snapshot(); r.sends() != 0 || len(checks) != 0 {
				t.Fatalf("key handler sent %d keys / took %d pane captures before the cmd ran", r.sends(), len(checks))
			}
			if last == nil {
				t.Fatal("final key returned a nil cmd; the dispatch must be a tea.Cmd")
			}
			askRunCmd(last)
			if r.sends() == 0 {
				t.Error("running the cmd sent nothing to the panes that show the menu")
			}
		})
	}
}

// Spec "Dispatching keys": "While a group dispatch is in flight, further
// approve/deny keys are ignored, so repeated presses do not send extra
// rounds."
func TestAskDispatch_RepeatPressWhileInFlightIsIgnored(t *testing.T) {
	h, r := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "Y")
	first := askKeyCmd(h, "y")
	if first == nil {
		t.Fatal("first approve-all returned nil cmd")
	}

	// Second round before the first result is delivered.
	var extra []tea.Cmd
	for _, k := range []string{"Y", "y", "N", "y", "y", "n"} {
		if cmd := askKeyCmd(h, k); cmd != nil {
			extra = append(extra, cmd)
		}
	}
	for _, cmd := range extra {
		for range askRunCmd(cmd) {
			t.Error("a dispatch issued while another was in flight produced a result")
		}
	}
	if len(extra) != 0 {
		t.Errorf("%d approve/deny cmds issued while a dispatch was in flight, want 0", len(extra))
	}
	if r.sends() != 0 {
		t.Fatalf("keys pressed during the in-flight dispatch sent %d keystrokes", r.sends())
	}

	// The first dispatch still completes normally, once.
	res := askOneResult(t, first)
	_, _ = h.Update(res)
	approved, _, _ := r.snapshot()
	if len(approved) != 2 {
		t.Fatalf("approved = %v, want exactly one round (2)", approved)
	}

	// Once its result is handled, approve-all works again.
	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "Y")
	if askKeyCmd(h, "y") == nil {
		t.Error("approve-all still blocked after the in-flight result was handled")
	}
}

// ---------- result message ----------

// Spec "Dispatching keys": "reports one result message: `Approved 2, skipped
// 19 (no prompt on screen), failed 0`".
func TestAskDispatch_ResultMessageApprovedFormat(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	_, _ = h.Update(askDispatchResultMsg{Action: "approve", Sent: 2, Skipped: 19, Failed: 0})
	if s := askStatusText(h); !strings.Contains(s, "Approved 2, skipped 19 (no prompt on screen), failed 0") {
		t.Errorf("status after approve result = %q, want it to contain %q", s, "Approved 2, skipped 19 (no prompt on screen), failed 0")
	}
}

// Spec: "(or `Denied ...`)".
func TestAskDispatch_ResultMessageDeniedFormat(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	_, _ = h.Update(askDispatchResultMsg{Action: "deny", Sent: 1, Skipped: 4, Failed: 0})
	if s := askStatusText(h); !strings.Contains(s, "Denied 1, skipped 4 (no prompt on screen), failed 0") {
		t.Errorf("status after deny result = %q, want it to contain %q", s, "Denied 1, skipped 4 (no prompt on screen), failed 0")
	}
}

// Spec: "Failures (send error, no tmux session) are counted and named, not
// swallowed."
func TestAskDispatch_ResultMessageNamesFailures(t *testing.T) {
	h, _, _ := newAskGroupsHome(t, askGroupsMixed()...)
	_, _ = h.Update(askDispatchResultMsg{Action: "approve", Sent: 1, Skipped: 0, Failed: 2, FailedTitles: []string{"title-ap1", "title-ap2"}})
	s := askStatusText(h)
	if !regexp.MustCompile(`failed 2`).MatchString(s) {
		t.Errorf("status = %q, want the failure count", s)
	}
	for _, title := range []string{"title-ap1", "title-ap2"} {
		if !strings.Contains(s, title) {
			t.Errorf("status = %q, want failed session %q named", s, title)
		}
	}
}

// Spec: a send error is counted as a failure and the session named, through
// the real dispatch cmd.
func TestAskDispatch_SendErrorCountedAndNamed(t *testing.T) {
	h, r := askMixHome(t)
	r.failSend["ap2"] = errors.New("tmux: no such session")
	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "Y")
	res := askOneResult(t, askKeyCmd(h, "y"))
	if res.Sent != 1 || res.Skipped != 3 || res.Failed != 1 {
		t.Fatalf("result = %+v, want Sent=1 Skipped=3 Failed=1", res)
	}
	if len(res.FailedTitles) != 1 || res.FailedTitles[0] != "title-ap2" {
		t.Errorf("FailedTitles = %v, want [title-ap2]", res.FailedTitles)
	}
	_, _ = h.Update(res)
	if s := askStatusText(h); !strings.Contains(s, "title-ap2") || !strings.Contains(s, "failed 1") {
		t.Errorf("status = %q, want failed 1 naming title-ap2", s)
	}
}

// End to end: the result of the mixed approve-all lands as the spec's line.
func TestAskDispatch_MixedApproveAllReportsSkipped(t *testing.T) {
	h, _ := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "Y")
	res := askOneResult(t, askKeyCmd(h, "y"))
	_, _ = h.Update(res)
	if s := askStatusText(h); !strings.Contains(s, "Approved 2, skipped 3 (no prompt on screen), failed 0") {
		t.Errorf("status = %q, want %q", s, "Approved 2, skipped 3 (no prompt on screen), failed 0")
	}
}

// ---------- confirmation count ----------

// Spec "Dispatching keys": "The confirmation prompt counts only rows still
// classified as approvals." ap3 has an open approval row but its cached
// substate no longer shows the menu.
func TestAskDispatch_ConfirmationCountsOnlyStillApprovals(t *testing.T) {
	h, _, _ := newAskGroupsHome(t,
		askGroupsSeed{"ap1", "approval", session.StatusWaiting, 1 * time.Minute},
		askGroupsSeed{"ap2", "approval", session.StatusWaiting, 2 * time.Minute},
		askGroupsSeed{"ap3", "approval", session.StatusWaiting, 3 * time.Minute},
	)
	installAskDispatchSeams(t, nil)
	setUICachedSubstate(t, uiMenuFor("ap1", "ap2"))
	h.refreshSessionRenderSnapshot(nil)

	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "Y")
	if p := askGroupsPromptLine(h); !strings.Contains(p, "Approve 2 pending tool calls? (y/n)") {
		t.Errorf("prompt = %q, want %q (ap3 is no longer an approval)", p, "Approve 2 pending tool calls? (y/n)")
	}
}

// Sanity for the fixture: a fleet of N approvals still shows N.
func TestAskDispatch_ConfirmationCountAllStillApprovals(t *testing.T) {
	h, _ := askMixHome(t)
	askGroupsMoveToItem(t, h, "ask-ap1")
	askKeyCmd(h, "N")
	want := fmt.Sprintf("Deny %d pending tool calls? (y/n)", 5)
	if p := askGroupsPromptLine(h); !strings.Contains(p, want) {
		t.Errorf("prompt = %q, want %q", p, want)
	}
}
