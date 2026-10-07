// Regression coverage for user report #3: "I pressed v to change to
// output-only mode, but it doesn't work." The `v` key cycles h.previewMode;
// PreviewModeOutput is supposed to hide the per-session info sections
// (Worktree, Claude) that only make sense in the richer "both" view. Modeled
// on TestPreview_HideWorktreeAndClaudeSections in preview_pane_test.go.
package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func newWorktreeClaudePreviewInstance() *session.Instance {
	inst := session.NewInstance("output-only-wt-claude", "/repos/app/.worktrees/feature-x")
	inst.Status = session.StatusRunning
	inst.Tool = "claude"
	inst.WorktreePath = "/repos/app/.worktrees/feature-x"
	inst.WorktreeRepoRoot = "/repos/app"
	inst.WorktreeBranch = "feature/x"
	return inst
}

// TestPreview_OutputOnlyMode_HidesWorktreeAndClaudeSections pins the contract:
// with h.previewMode == PreviewModeOutput, the Worktree and Claude section
// dividers must not appear, even when previewHideWorktree/previewHideClaude
// are both false (the per-session sections, not just the config toggles,
// gate on output-only mode).
func TestPreview_OutputOnlyMode_HidesWorktreeAndClaudeSections(t *testing.T) {
	h := homeWithSession(newWorktreeClaudePreviewInstance())
	h.previewHideWorktree = false
	h.previewHideClaude = false
	h.previewMode = PreviewModeOutput

	rendered := tmux.StripANSI(h.renderPreviewPane(100, 40))

	if strings.Contains(rendered, "Worktree") {
		t.Errorf("PreviewModeOutput should hide the Worktree section\nrendered=%q", rendered)
	}
	if strings.Contains(rendered, "Claude") {
		t.Errorf("PreviewModeOutput should hide the Claude section\nrendered=%q", rendered)
	}
}

// TestPreview_BothMode_ShowsWorktreeAndClaudeSections is the control case:
// PreviewModeBoth (the default `v` cycles away from and back to) must still
// show both sections when the hide toggles are off, so the output-only
// behaviour above is a genuine mode-specific change rather than the sections
// having vanished for everyone.
func TestPreview_BothMode_ShowsWorktreeAndClaudeSections(t *testing.T) {
	h := homeWithSession(newWorktreeClaudePreviewInstance())
	h.previewHideWorktree = false
	h.previewHideClaude = false
	h.previewMode = PreviewModeBoth

	rendered := tmux.StripANSI(h.renderPreviewPane(100, 40))

	if !strings.Contains(rendered, "Worktree") {
		t.Errorf("PreviewModeBoth should show the Worktree section\nrendered=%q", rendered)
	}
	if !strings.Contains(rendered, "Claude") {
		t.Errorf("PreviewModeBoth should show the Claude section\nrendered=%q", rendered)
	}
}
