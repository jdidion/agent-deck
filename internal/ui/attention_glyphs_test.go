package ui

// Spec: docs/design/2026-10-23-ask-groups.md, section "Session row icons".
//
// Seams these tests assume (the implementer adds them):
//   - sessionRenderState.askKind session.AskKind, filled by
//     refreshSessionRenderSnapshot from the instance's status and its cached
//     hook record (Instance.UpdateHookStatus must retain Matcher/ToolName).
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
// time". The snapshot carries the classification.
func TestRenderSnapshot_ClassifiesFromCachedHookRecord(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name   string
		status session.Status
		hs     *session.HookStatus
		want   session.AskKind
	}{
		{"PreToolUse Bash while waiting -> approval", session.StatusWaiting,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}, session.AskApproval},
		{"PreToolUse AskUserQuestion while waiting -> decision", session.StatusWaiting,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "AskUserQuestion", UpdatedAt: now}, session.AskDecision},
		{"permission_prompt notification -> approval", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Notification", Matcher: "permission_prompt", UpdatedAt: now}, session.AskApproval},
		{"elicitation_dialog notification -> decision", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Notification", Matcher: "elicitation_dialog", UpdatedAt: now}, session.AskDecision},
		{"Stop while waiting -> not an ask", session.StatusWaiting,
			&session.HookStatus{Status: "waiting", Event: "Stop", UpdatedAt: now}, ""},
		{"PreToolUse while running -> not an ask", session.StatusRunning,
			&session.HookStatus{Status: "running", Event: "PreToolUse", ToolName: "Bash", UpdatedAt: now}, ""},
		{"error status -> error", session.StatusError, nil, session.AskError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

// attentionFleet: 2 approval, 1 decision, 1 plain waiting, 1 running.
func attentionFleet() []*session.Instance {
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
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: attentionFleet()}).home
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
	h := NewGateHome(GateHomeOptions{Width: 200, Instances: attentionFleet()}).home
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
