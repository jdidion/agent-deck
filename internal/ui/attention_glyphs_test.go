package ui

// Spec: docs/design/2026-10-23-ask-groups.md, section "Session row icons".
//
// Seams these tests assume (the implementer adds them):
//   - sessionRenderState.askKind session.AskKind, filled by
//     refreshSessionRenderSnapshot from the instance's status and its cached
//     hook record (Instance.UpdateHookStatus must retain Matcher/ToolName)
//     and, per the revised spec "Kinds", the cached substate read through
//     uiCachedSubstate (see ask_dispatch_test.go).
//   - rowAttentionGlyph(status session.Status, substate session.Substate,
//     archived bool, kind session.AskKind) (icon string, style lipgloss.Style)
//     used by renderSessionItem.

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Spec "Session row icons" table: glyph and color per state.
func TestRowAttentionGlyph_SpecTable(t *testing.T) {
	tests := []struct {
		name   string
		status session.Status
		kind   session.AskKind
		icon   string
		color  lipgloss.Color
	}{
		{"running", session.StatusRunning, "", "●", ColorGreen},
		{"needs approval", session.StatusWaiting, session.AskApproval, "?", ColorYellow},
		{"waiting, your turn", session.StatusWaiting, "", "◐", ColorTextDim},
		{"idle", session.StatusIdle, "", "○", ColorTextDim},
		{"error", session.StatusError, session.AskError, "✕", ColorRed},
		{"stopped", session.StatusStopped, "", "■", ColorTextDim},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			icon, style := rowAttentionGlyph(tt.status, session.SubstateNone, false, tt.kind)
			if icon != tt.icon {
				t.Errorf("icon = %q, want %q", icon, tt.icon)
			}
			if got := style.GetForeground(); got != tt.color {
				t.Errorf("color = %v, want %v", got, tt.color)
			}
		})
	}
}

// Spec "Session row icons": needs decision is "✎" in magenta. The palette has
// no color named magenta, so this pins only that the color is distinct from
// every other state's color.
func TestRowAttentionGlyph_DecisionIsDistinctMagenta(t *testing.T) {
	icon, style := rowAttentionGlyph(session.StatusWaiting, session.SubstateNone, false, session.AskDecision)
	if icon != "✎" {
		t.Errorf("icon = %q, want %q", icon, "✎")
	}
	fg := style.GetForeground()
	for _, other := range []lipgloss.Color{ColorGreen, ColorYellow, ColorRed, ColorTextDim} {
		if fg == other {
			t.Errorf("decision color %v collides with another state's color", fg)
		}
	}
	if _, isNoColor := fg.(lipgloss.NoColor); isNoColor {
		t.Error("decision glyph has no color; spec wants magenta")
	}
}

// Spec "Session row icons": archived rows keep the stopped glyph regardless of
// a stale attention state (existing rowStatusGlyph contract, preserved).
func TestRowAttentionGlyph_ArchivedIsStopped(t *testing.T) {
	icon, _ := rowAttentionGlyph(session.StatusWaiting, session.SubstateNone, true, session.AskApproval)
	if icon != "■" {
		t.Errorf("archived icon = %q, want ■", icon)
	}
}

// renderAttentionRow renders one session row from a hand-built snapshot.
func renderAttentionRow(status session.Status, kind session.AskKind) string {
	h := &Home{width: 100}
	inst := &session.Instance{ID: "row", Title: "row-title", Tool: "claude", Status: status}
	snap := map[string]sessionRenderState{"row": {status: status, tool: "claude", askKind: kind}}
	var b strings.Builder
	h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true}, false, snap, h.width)
	return tmux.StripANSI(b.String())
}

// Spec "Session row icons": the rendered row shows the state's glyph.
func TestRenderSessionItem_DrawsAttentionGlyph(t *testing.T) {
	tests := []struct {
		name   string
		status session.Status
		kind   session.AskKind
		want   string
		absent string
	}{
		{"approval", session.StatusWaiting, session.AskApproval, "?", "◐"},
		{"decision", session.StatusWaiting, session.AskDecision, "✎", "◐"},
		{"your turn", session.StatusWaiting, "", "◐", "✎"},
		{"running", session.StatusRunning, "", "●", "◐"},
		{"error", session.StatusError, session.AskError, "✕", "◐"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := renderAttentionRow(tt.status, tt.kind)
			if !strings.Contains(row, tt.want) {
				t.Errorf("row missing glyph %q: %q", tt.want, row)
			}
			if strings.Contains(row, tt.absent) {
				t.Errorf("row should not contain %q: %q", tt.absent, row)
			}
		})
	}
}

// Spec "Session row icons": "The row uses the same classification as the ask
// queue, computed from the session's status and cached hook record at render
// time". The snapshot carries the classification. Revised spec "Kinds": an
// approval or decision also needs the cached substate interactive-menu.
func TestRenderSnapshot_ClassifiesFromCachedHookRecord(t *testing.T) {
	now := time.Now()
	menu, none := session.SubstateInteractiveMenu, session.SubstateNone
	tests := []struct {
		name   string
		status session.Status
		hs     *session.HookStatus
		sub    session.Substate
		want   session.AskKind
	}{
		{"PreToolUse Bash while waiting with menu -> approval", session.StatusWaiting,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}, menu, session.AskApproval},
		// Live check: 19 of 21 such sessions were at their normal prompt.
		{"REGRESSION PreToolUse Bash while waiting, no menu -> not an ask", session.StatusWaiting,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}, none, ""},
		{"PreToolUse AskUserQuestion while waiting with menu -> decision", session.StatusWaiting,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "AskUserQuestion", UpdatedAt: now}, menu, session.AskDecision},
		{"permission_prompt notification with menu -> approval", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Notification", Matcher: "permission_prompt", UpdatedAt: now}, menu, session.AskApproval},
		{"permission_prompt notification, no menu -> not an ask", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Notification", Matcher: "permission_prompt", UpdatedAt: now}, none, ""},
		{"elicitation_dialog notification with menu -> decision", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Notification", Matcher: "elicitation_dialog", UpdatedAt: now}, menu, session.AskDecision},
		{"Stop while waiting, no menu -> not an ask", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: now}, none, ""},
		{"PreToolUse while running -> not an ask", session.StatusRunning,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}, menu, ""},
		{"error status -> error", session.StatusError, nil, none, session.AskError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := tt.sub
			setUICachedSubstate(t, func(*session.Instance) session.Substate { return sub })
			inst := &session.Instance{ID: "snap", Title: "snap", Tool: "claude", Status: tt.status}
			if tt.hs != nil {
				inst.UpdateHookStatus(tt.hs)
			}
			h := NewGateHome(GateHomeOptions{Instances: []*session.Instance{inst}}).home
			h.refreshSessionRenderSnapshot(nil)
			state, ok := h.getSessionRenderSnapshot()["snap"]
			if !ok {
				t.Fatal("instance missing from render snapshot")
			}
			if state.askKind != tt.want {
				t.Errorf("snapshot askKind = %q, want %q", state.askKind, tt.want)
			}
		})
	}
}

// attentionFleet: 2 approval, 1 decision, 1 plain waiting, 1 running. The
// cached substate shows a menu for ap1, ap2 and de1 only (spec "Kinds").
func attentionFleet(t *testing.T) []*session.Instance {
	t.Helper()
	setUICachedSubstate(t, uiMenuFor("ap1", "ap2", "de1"))
	now := time.Now()
	mk := func(id string, st session.Status, hs *session.HookStatus) *session.Instance {
		inst := &session.Instance{ID: id, Title: id, Tool: "claude", Status: st}
		if hs != nil {
			inst.UpdateHookStatus(hs)
		}
		return inst
	}
	return []*session.Instance{
		mk("ap1", session.StatusWaiting, &session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}),
		mk("ap2", session.StatusWaiting, &session.HookStatus{Status: "waiting", Event: "PermissionRequest", UpdatedAt: now}),
		mk("de1", session.StatusWaiting, &session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "AskUserQuestion", UpdatedAt: now}),
		mk("wt1", session.StatusWaiting, &session.HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: now}),
		mk("ru1", session.StatusRunning, nil),
	}
}

// Spec "Session row icons": "Header counts (`● 13 ◐ 6`) gain separate counts
// for `?` and `✎` when non-zero."
func TestFilterBar_ShowsApprovalAndDecisionCounts(t *testing.T) {
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: attentionFleet(t)}).home
	bar := tmux.StripANSI(h.renderFilterBar())
	if !regexp.MustCompile(`\?\s*2\b`).MatchString(bar) {
		t.Errorf("filter bar missing approval count \"? 2\": %q", bar)
	}
	if !regexp.MustCompile(`✎\s*1\b`).MatchString(bar) {
		t.Errorf("filter bar missing decision count \"✎ 1\": %q", bar)
	}
	if !regexp.MustCompile(`●\s*1\b`).MatchString(bar) {
		t.Errorf("filter bar running count should stay \"● 1\": %q", bar)
	}
}

// Spec "Session row icons": the counts are "separate", and ◐ now means only
// "waiting, your turn (not an ask)", so asks are not double-counted under ◐.
func TestFilterBar_WaitingCountExcludesAsks(t *testing.T) {
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: attentionFleet(t)}).home
	bar := tmux.StripANSI(h.renderFilterBar())
	if !regexp.MustCompile(`◐\s*1\b`).MatchString(bar) {
		t.Errorf("◐ count should be 1 (only the finished-turn session): %q", bar)
	}
}

// Spec "Session row icons": the `?` and `✎` counts appear only "when non-zero".
func TestFilterBar_OmitsZeroApprovalAndDecisionCounts(t *testing.T) {
	inst := &session.Instance{ID: "ru1", Title: "ru1", Tool: "claude", Status: session.StatusRunning}
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: []*session.Instance{inst}}).home
	bar := tmux.StripANSI(h.renderFilterBar())
	if regexp.MustCompile(`\?\s*\d`).MatchString(bar) {
		t.Errorf("filter bar shows an approval count with no approvals: %q", bar)
	}
	if strings.Contains(bar, "✎") {
		t.Errorf("filter bar shows a decision count with no decisions: %q", bar)
	}
}

// Spec "Session row icons": "Help lists the glyphs."
func TestHelpOverlay_ListsAttentionGlyphs(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(120, 400)
	overlay.Show()
	view := tmux.StripANSI(overlay.View())
	lower := strings.ToLower(view)
	if !strings.Contains(view, "✎") {
		t.Error("help does not list the ✎ (needs decision) glyph")
	}
	if !strings.Contains(lower, "approval") {
		t.Error("help does not describe the ? (needs approval) glyph")
	}
	if !strings.Contains(lower, "decision") {
		t.Error("help does not describe the ✎ (needs decision) glyph")
	}
}

// Live check (spec "Kinds" revision): a waiting session whose last hook is
// PreToolUse but whose pane shows no menu is "your turn" (◐), not ?.
func TestFilterBar_StalePreToolUseWithoutMenuCountsAsWaiting(t *testing.T) {
	setUICachedSubstate(t, uiMenuFor())
	inst := &session.Instance{ID: "st1", Title: "st1", Tool: "claude", Status: session.StatusWaiting}
	inst.UpdateHookStatus(&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: time.Now()})
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: []*session.Instance{inst}}).home
	bar := tmux.StripANSI(h.renderFilterBar())
	if regexp.MustCompile(`\?\s*\d`).MatchString(bar) {
		t.Errorf("stale PreToolUse with no menu counted as an approval: %q", bar)
	}
	if !regexp.MustCompile(`◐\s*1\b`).MatchString(bar) {
		t.Errorf("◐ count should be 1 for the stale PreToolUse session: %q", bar)
	}
}

// Spec "Other fixes from review": "The filter bar does not count archived
// sessions as `?` or `✎`." The archived sessions are not superseded, so the
// existing archivedSuperseded exclusion does not cover them.
func TestFilterBar_ArchivedSessionsNotCountedAsAsks(t *testing.T) {
	fleet := attentionFleet(t)
	now := time.Now()
	mk := func(id, tool string) *session.Instance {
		inst := &session.Instance{ID: id, Title: id, Tool: "claude", Status: session.StatusWaiting}
		inst.UpdateHookStatus(&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: tool, UpdatedAt: now})
		inst.SetArchivedAtThreadSafe(now.Add(-time.Hour))
		return inst
	}
	fleet = append(fleet, mk("arch-ap", "Bash"), mk("arch-de", "AskUserQuestion"))
	setUICachedSubstate(t, uiMenuFor("ap1", "ap2", "de1", "arch-ap", "arch-de"))
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: fleet}).home
	h.refreshSessionRenderSnapshot(nil)
	bar := tmux.StripANSI(h.renderFilterBar())
	if !regexp.MustCompile(`\?\s*2\b`).MatchString(bar) {
		t.Errorf("approval count should stay 2 with an archived approval present: %q", bar)
	}
	if !regexp.MustCompile(`✎\s*1\b`).MatchString(bar) {
		t.Errorf("decision count should stay 1 with an archived decision present: %q", bar)
	}
}

// Spec "Second review round", bullet "Counts": "Archived sessions count in
// none of the ●/?/✎/◐ pills' ask arithmetic" (finding f19: an archived
// session on a menu dropped out of ? and landed in ◐). attentionFleet has one
// your-turn session, so ◐ must stay 1.
func TestFilterBar_ArchivedApprovalNotCountedAsWaiting(t *testing.T) {
	fleet := attentionFleet(t)
	now := time.Now()
	arch := &session.Instance{ID: "arch-ap", Title: "arch-ap", Tool: "claude", Status: session.StatusWaiting}
	arch.UpdateHookStatus(&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now})
	arch.SetArchivedAtThreadSafe(now.Add(-time.Hour))
	fleet = append(fleet, arch)
	setUICachedSubstate(t, uiMenuFor("ap1", "ap2", "de1", "arch-ap"))
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: fleet}).home
	h.refreshSessionRenderSnapshot(nil)
	bar := tmux.StripANSI(h.renderFilterBar())
	if !regexp.MustCompile(`◐\s*1\b`).MatchString(bar) {
		t.Errorf("◐ should stay 1 with an archived approval present (it is in neither ◐ nor ?): %q", bar)
	}
}

// Same bullet: "an archived waiting session is not "your turn" either".
func TestFilterBar_ArchivedYourTurnNotCountedAsWaiting(t *testing.T) {
	fleet := attentionFleet(t)
	now := time.Now()
	arch := &session.Instance{ID: "arch-wt", Title: "arch-wt", Tool: "claude", Status: session.StatusWaiting}
	arch.UpdateHookStatus(&session.HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: now})
	arch.SetArchivedAtThreadSafe(now.Add(-time.Hour))
	fleet = append(fleet, arch)
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: fleet}).home
	h.refreshSessionRenderSnapshot(nil)
	bar := tmux.StripANSI(h.renderFilterBar())
	if !regexp.MustCompile(`◐\s*1\b`).MatchString(bar) {
		t.Errorf("◐ should stay 1 with an archived your-turn session present: %q", bar)
	}
}
