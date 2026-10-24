package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// Spec docs/design/2026-10-23-ask-groups.md "Dispatching keys": dispatch
// "requires `hasOpenInteractiveMenu` to hold for it (exported for the UI as
// `tmux.HasOpenInteractiveMenu`)". The export must agree with the internal
// predicate on realistic panes; the fixtures are the ones the internal
// predicate is already pinned against (#2473).
func TestHasOpenInteractiveMenu_ExportMatchesInternalOnFixtures(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{"background_work/workflow-permission-menu.txt", true}, // Bash permission dialog
		{"background_work/workflow-ask-question.txt", true},    // AskUserQuestion picker
		{"background_work/workflow-prose-question.txt", false}, // reply ending in a prose question
		{"background_work/workflow-finished.txt", false},
		{"pane_status/claude-idle.txt", false}, // normal prompt: the stray-"1" case
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatalf("fixture: %v", err)
			}
			content := StripANSI(string(b))
			if got := HasOpenInteractiveMenu(content); got != c.want {
				t.Errorf("HasOpenInteractiveMenu = %v, want %v", got, c.want)
			}
			if HasOpenInteractiveMenu(content) != hasOpenInteractiveMenu(content) {
				t.Error("export disagrees with hasOpenInteractiveMenu")
			}
		})
	}
}

// Same predicate on the inline frames of TestHasOpenInteractiveMenu_StrictQuestions.
func TestHasOpenInteractiveMenu_ExportStrictQuestions(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"idle prompt", "⏺ Done.\n\n────────\n❯ \n────────\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n", false},
		{"prose question", "⏺ Done.\n\n  Would you like me to push the branch?\n\n❯ \n  ⏵⏵ bypass permissions on", false},
		{"permission dialog", " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n", true},
		{"picker chrome", "❯ 1. staging\n  2. production\n\nEnter to select · ↑/↓ to navigate · Esc to cancel\n", true},
	}
	for _, c := range cases {
		if got := HasOpenInteractiveMenu(c.content); got != c.want {
			t.Errorf("%s: HasOpenInteractiveMenu = %v, want %v", c.name, got, c.want)
		}
	}
}
