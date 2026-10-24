package tmux

// Spec: docs/design/2026-10-23-ask-groups.md, "Second review round
// (2026-10-24)", bullet "Approve answers only a permission prompt" (review
// findings f4 and f16).
//
// Seam these tests assume (the implementer adds it):
//   - PaneShowsPermissionPrompt(content string) bool, exported. It reports
//     whether the frame shows a permission dialog (the numbered Yes/No
//     "Do you want to proceed?" shape, including plan approval), and not any
//     other open menu. It applies the same frame preparation as the status
//     classifier (prepareFrame, i.e. trimClaudeTrailingRoster for Claude)
//     itself, so callers pass the ANSI-stripped capture as is.
//
// Fixture sources: files under testdata/ are real captures; the inline frames
// marked "constructed" are written for this test from the shapes those
// captures show.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func permissionFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return StripANSI(string(b))
}

// permissionDialogWithRoster is a Bash permission dialog (the body of
// background_work/workflow-permission-menu.txt) followed by Claude's footer
// and an agent roster of rosterRows non-empty rows. Constructed.
func permissionDialogWithRoster(rosterRows int) string {
	var b strings.Builder
	b.WriteString("⏺ Bash(git push origin main)\n\n")
	b.WriteString("────────────────────────────────────────\n")
	b.WriteString(" Bash command\n\n")
	b.WriteString("   git push origin main\n")
	b.WriteString("   Push the branch to origin\n\n")
	b.WriteString(" Do you want to proceed?\n")
	b.WriteString(" ❯ 1. Yes\n")
	b.WriteString("   2. Yes, and don't ask again for git push commands in /tmp/probe-proj\n")
	b.WriteString("   3. No, and tell Claude what to do differently (esc)\n\n")
	b.WriteString(" Esc to cancel · Tab to amend\n\n")
	b.WriteString("  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n\n")
	b.WriteString("  ⏺ main\n")
	for i := 1; i < rosterRows; i++ {
		fmt.Fprintf(&b, "  ◯ general-purpose  Task %d\n", i)
	}
	return b.String()
}

// ---------- accepted: permission dialogs ----------

// Spec: the gate "requires the pane to show a permission dialog (the numbered
// Yes/No "Do you want to proceed?" shape". Real capture of a Bash dialog.
func TestPaneShowsPermissionPrompt_AcceptsBashPermissionDialogCapture(t *testing.T) {
	if !PaneShowsPermissionPrompt(permissionFixture(t, "background_work/workflow-permission-menu.txt")) {
		t.Fatal("PaneShowsPermissionPrompt = false on a captured Bash permission dialog")
	}
}

// Spec: same shape, minimal frame (from TestHasOpenInteractiveMenu_StrictQuestions).
func TestPaneShowsPermissionPrompt_AcceptsMinimalYesNoDialog(t *testing.T) {
	if !PaneShowsPermissionPrompt(" Do you want to proceed?\n ❯ 1. Yes\n   2. No\n") {
		t.Fatal("PaneShowsPermissionPrompt = false on a minimal Do-you-want-to-proceed dialog")
	}
}

// Spec: a permission dialog, the edit-approval variant. Constructed.
func TestPaneShowsPermissionPrompt_AcceptsEditPermissionDialog(t *testing.T) {
	frame := "⏺ Update(main.go)\n\n" +
		"────────────────────────────────────────\n" +
		" Edit file\n main.go\n\n" +
		" Do you want to make this edit to main.go?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, allow all edits during this session (shift+tab)\n" +
		"   3. No, and tell Claude what to do differently (esc)\n"
	if !PaneShowsPermissionPrompt(frame) {
		t.Fatal("PaneShowsPermissionPrompt = false on an edit permission dialog")
	}
}

// Spec: "including plan approval" (ExitPlanMode is an approval, spec "Kinds").
// Constructed.
func TestPaneShowsPermissionPrompt_AcceptsPlanApprovalDialog(t *testing.T) {
	frame := "⏺ ExitPlanMode(Add the permission predicate)\n\n" +
		"────────────────────────────────────────\n" +
		" Ready to code?\n\n" +
		" Here is Claude's plan:\n" +
		"  1. Add the predicate\n  2. Wire it into dispatch\n\n" +
		" Would you like to proceed?\n\n" +
		" ❯ 1. Yes, and auto-accept edits\n" +
		"   2. Yes, and manually approve edits\n" +
		"   3. No, keep planning\n"
	if !PaneShowsPermissionPrompt(frame) {
		t.Fatal("PaneShowsPermissionPrompt = false on a plan-approval dialog")
	}
}

// Spec: "The check uses the same frame preparation as the status classifier
// (prepareFrame), so Claude's agent roster below the footer cannot push the
// dialog out of the inspected tail" (f16). 16 non-empty roster rows push
// every dialog line out of a raw 15-line tail.
func TestPaneShowsPermissionPrompt_AcceptsDialogAboveLongAgentRoster(t *testing.T) {
	frame := permissionDialogWithRoster(16)
	// Precondition: the raw tail predicate loses the dialog, which is the bug.
	if HasOpenInteractiveMenu(frame) {
		t.Fatal("precondition: the roster is too short to push the dialog out of the raw 15-line tail")
	}
	if !PaneShowsPermissionPrompt(frame) {
		t.Fatal("PaneShowsPermissionPrompt = false for a permission dialog above a 16-row agent roster")
	}
}

// ---------- rejected: other selection lists and no menu ----------

// Spec: "an AskUserQuestion picker or other selection list is skipped, so `1`
// never picks a question's first option" (f4). Real capture.
func TestPaneShowsPermissionPrompt_RejectsAskUserQuestionPickerCapture(t *testing.T) {
	if PaneShowsPermissionPrompt(permissionFixture(t, "background_work/workflow-ask-question.txt")) {
		t.Fatal("PaneShowsPermissionPrompt = true on a captured AskUserQuestion picker")
	}
}

// Spec (f4): a question picker whose options happen to read Yes/No, and whose
// question starts "Do you want", is still a picker, not a permission dialog.
// Constructed from the picker capture's chrome.
func TestPaneShowsPermissionPrompt_RejectsYesNoQuestionPicker(t *testing.T) {
	frame := "⏺ User answered nothing yet.\n\n" +
		"────────────────────────────────────────\n" +
		" ☐ Push\n\n" +
		"Do you want me to push the branch now?\n\n" +
		"❯ 1. Yes\n" +
		"     Push to origin\n" +
		"  2. No\n" +
		"     Keep it local\n" +
		"  3. Type something.\n\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel\n"
	if PaneShowsPermissionPrompt(frame) {
		t.Fatal("PaneShowsPermissionPrompt = true on an AskUserQuestion picker with Yes/No options")
	}
}

// Spec: "other selection list". Inline picker chrome from
// TestHasOpenInteractiveMenu_ExportStrictQuestions.
func TestPaneShowsPermissionPrompt_RejectsGenericPicker(t *testing.T) {
	if PaneShowsPermissionPrompt("❯ 1. staging\n  2. production\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n") {
		t.Fatal("PaneShowsPermissionPrompt = true on a generic selection list")
	}
}

// Spec: "other selection list": the end-of-session feedback survey (an open
// picker per interactiveMenuMarkers). Typing "1" into it answers the survey.
// Constructed.
func TestPaneShowsPermissionPrompt_RejectsFeedbackSurvey(t *testing.T) {
	frame := "⏺ Done.\n\n" +
		"● How is Claude doing this session? (optional)\n" +
		"  1: Bad    2: Fine   3: Good   0: Dismiss\n\n" +
		"────────\n❯ \n────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
	if PaneShowsPermissionPrompt(frame) {
		t.Fatal("PaneShowsPermissionPrompt = true on the feedback survey")
	}
}

// Driving report: "after hitting y they complain about getting a stray "1"
// character". A normal idle prompt is not a permission dialog. Real capture.
func TestPaneShowsPermissionPrompt_RejectsIdlePrompt(t *testing.T) {
	if PaneShowsPermissionPrompt(permissionFixture(t, "pane_status/claude-idle.txt")) {
		t.Fatal("PaneShowsPermissionPrompt = true on an idle prompt")
	}
}

// A reply that ends in a prose question is not a dialog. Real capture.
func TestPaneShowsPermissionPrompt_RejectsProseQuestion(t *testing.T) {
	if PaneShowsPermissionPrompt(permissionFixture(t, "background_work/workflow-prose-question.txt")) {
		t.Fatal("PaneShowsPermissionPrompt = true on a reply ending in a prose question")
	}
}

// Same rule with the long roster: trimming must not make a picker above the
// footer read as a permission dialog. Constructed.
func TestPaneShowsPermissionPrompt_RejectsPickerAboveLongAgentRoster(t *testing.T) {
	var b strings.Builder
	b.WriteString(" ☐ Deploy target\n\nWhich environment should I deploy to?\n\n")
	b.WriteString("❯ 1. staging\n  2. production\n  3. Type something.\n\n")
	b.WriteString("Enter to select · ↑/↓ to navigate · Esc to cancel\n\n")
	b.WriteString("  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n\n  ⏺ main\n")
	for i := 1; i < 16; i++ {
		fmt.Fprintf(&b, "  ◯ general-purpose  Task %d\n", i)
	}
	if PaneShowsPermissionPrompt(b.String()) {
		t.Fatal("PaneShowsPermissionPrompt = true on a picker above an agent roster")
	}
}
