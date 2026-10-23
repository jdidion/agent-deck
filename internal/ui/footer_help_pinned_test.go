// Regression coverage for user report #1: "The shortcut to hide Worktree and
// Claude panes isn't on the visible shortcut list - is there a shortcut to
// open the full list of commands? If so, it needs to always be visible on the
// shortcut list." Help (the key that opens the full command list) must stay
// in the footer across every footer style (session.FooterFull/Compact/
// Minimal/Curated) and every width, even when other hints get trimmed.
package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// footerHelpPinnedSession builds a selected session row with many context
// hints bound (fork, MCP manager, skills, sandbox exec, multi-repo paths,
// restart-fresh, notes) so the width-fitting logic in every footer style has
// plenty of material to trim before it would ever need to touch Help.
func footerHelpPinnedSession() *session.Instance {
	inst := session.NewInstanceWithTool("footer-help-pinned", "/tmp/footer-help-pinned", "claude")
	inst.Status = session.StatusRunning
	inst.ClaudeSessionID = "sess-1234"
	inst.ClaudeDetectedAt = time.Now()
	inst.MultiRepoEnabled = true
	inst.Sandbox = &session.SandboxConfig{Enabled: true}
	return inst
}

func footerHelpPinnedHome(width int, mode string) *Home {
	home := NewHome()
	home.width = width
	home.height = 40
	home.footerMode = mode

	inst := footerHelpPinnedSession()
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()

	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.cursor = 0
	return home
}

// TestFooterHelpAlwaysVisible_AcrossModesAndWidths pins the core contract:
// whatever footer style is configured, and whatever the terminal width, the
// rendered footer (ANSI-stripped) must contain the Help key.
func TestFooterHelpAlwaysVisible_AcrossModesAndWidths(t *testing.T) {
	modes := []string{
		session.FooterFull,
		session.FooterCompact,
		session.FooterMinimal,
		session.FooterCurated,
	}

	for _, mode := range modes {
		for width := 50; width <= 220; width += 10 {
			home := footerHelpPinnedHome(width, mode)
			helpKey := home.actionKey(hotkeyHelp)
			if helpKey == "" {
				t.Fatalf("mode=%s width=%d: default hotkeys produced an empty Help key", mode, width)
			}

			rendered := tmux.StripANSI(home.renderHelpBar())
			if !strings.Contains(rendered, helpKey) {
				t.Errorf("mode=%s width=%d: footer dropped the Help key %q\nrendered=%q", mode, width, helpKey, rendered)
			}

			for _, line := range strings.Split(rendered, "\n") {
				if cellWidth(line) > home.width {
					t.Errorf("mode=%s width=%d: footer line exceeds width (%d cells): %q", mode, width, cellWidth(line), line)
				}
			}
		}
	}
}

// TestFooterHelpSurvives_CompactTrimsOtherGlobalHintsFirst pins that in the
// compact bar specifically, Help outlives the other global hints (Search,
// Settings, Quit) when the bar is too narrow for all of them: at a narrow
// width those are expected to be trimmed, but Help must remain.
func TestFooterHelpSurvives_CompactTrimsOtherGlobalHintsFirst(t *testing.T) {
	home := footerHelpPinnedHome(72, session.FooterCompact)
	helpKey := home.actionKey(hotkeyHelp)
	if helpKey == "" {
		t.Fatal("default hotkeys produced an empty Help key")
	}

	rendered := tmux.StripANSI(home.renderHelpBarCompact())
	if !strings.Contains(rendered, helpKey+" Help") {
		t.Errorf("compact footer at width=72 dropped %q Help: %q", helpKey, rendered)
	}
}

// TestFooterHelpSurvives_MinimalTruncatesContextKeysWithEllipsis pins the
// minimal bar's contract: when content is too wide, the right side keeps
// "<key> help" and the left (context) keys are truncated with "…" instead of
// Help being sacrificed. A long jump-mode buffer is used to force the
// too-wide case deterministically (the fixed context-key set used elsewhere
// in this file is never wide enough on its own to trigger the squeeze path
// at any width the Minimal tier renders at).
func TestFooterHelpSurvives_MinimalTruncatesContextKeysWithEllipsis(t *testing.T) {
	home := footerHelpPinnedHome(55, session.FooterMinimal)
	home.jumpMode = true
	home.jumpBuffer = strings.Repeat("abcdefghijklmnopqrstuvwxyz", 2)
	helpKey := home.actionKey(hotkeyHelp)
	if helpKey == "" {
		t.Fatal("default hotkeys produced an empty Help key")
	}

	rendered := tmux.StripANSI(home.renderHelpBarMinimal())
	if !strings.Contains(rendered, helpKey+" help") {
		t.Errorf("minimal footer at width=55 should keep %q help on the right side: %q", helpKey, rendered)
	}
	if !strings.Contains(rendered, "…") {
		t.Errorf("minimal footer at width=55 should truncate the context keys with an ellipsis: %q", rendered)
	}

	for _, line := range strings.Split(rendered, "\n") {
		if cellWidth(line) > home.width {
			t.Errorf("minimal footer line exceeds width %d (%d cells): %q", home.width, cellWidth(line), line)
		}
	}
}
