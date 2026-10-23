package ui

// Spec: docs/design/2026-10-23-ask-groups.md, section "Preview refresh after
// detach": "When the user detaches from a session back to the list, the
// preview for the selected session is refetched immediately (its cached
// capture invalidated and a fetch issued on the attach-return path), so the
// first frame after return shows current output rather than the pre-attach
// capture."

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

const stalePreAttachCapture = "STALE-PRE-ATTACH-CAPTURE"

// collectPreviewFetchKeys runs cmd (walking tea.Batch trees) and returns the
// preview keys of every preview fetch it issues, direct or debounced.
func collectPreviewFetchKeys(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	var keys []string
	var walk func(tea.Msg)
	walk = func(msg tea.Msg) {
		switch m := msg.(type) {
		case tea.BatchMsg:
			for _, c := range m {
				if c != nil {
					walk(c())
				}
			}
		case previewFetchedMsg:
			keys = append(keys, m.previewKey)
		case previewDebounceMsg:
			keys = append(keys, m.previewKey)
		}
	}
	walk(cmd())
	return keys
}

func seedStalePreview(h *Home, key string) {
	h.previewCacheMu.Lock()
	h.previewCache[key] = stalePreAttachCapture
	h.previewCacheTime[key] = time.Now()
	h.previewCacheMu.Unlock()
}

// Spec: "its cached capture invalidated ... on the attach-return path". Checked
// before any returned Cmd runs, i.e. synchronously in Update.
func TestAttachReturn_InvalidatesSelectedPreviewCache(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	h.cursor = 0
	seedStalePreview(h, sel.ID)

	_, _ = h.Update(statusUpdateMsg{attachedSessionID: sel.ID})

	h.previewCacheMu.RLock()
	got, ok := h.previewCache[sel.ID]
	h.previewCacheMu.RUnlock()
	if ok && got == stalePreAttachCapture {
		t.Fatal("attach return kept the pre-attach capture cached for the selected session")
	}
}

// Spec: "and a fetch issued on the attach-return path".
func TestAttachReturn_IssuesPreviewFetchForSelectedSession(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	h.cursor = 0
	seedStalePreview(h, sel.ID)

	_, cmd := h.Update(statusUpdateMsg{attachedSessionID: sel.ID})

	keys := collectPreviewFetchKeys(cmd)
	found := false
	for _, k := range keys {
		if k == sel.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("attach-return Cmd issued no preview fetch for %q (fetch keys: %v)", sel.ID, keys)
	}
}

// Spec: "so the first frame after return shows current output rather than the
// pre-attach capture".
func TestAttachReturn_FirstFrameDoesNotShowPreAttachCapture(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	// A live session, so the preview pane renders its cached capture. The
	// returned Cmd (which would reconcile status) is never run here.
	sel.Status = session.StatusRunning
	h.refreshSessionRenderSnapshot(nil)
	h.cursor = 0
	seedStalePreview(h, sel.ID)
	if !strings.Contains(h.View(), stalePreAttachCapture) {
		t.Fatal("precondition: the preview pane should render the cached capture before detach")
	}

	_, _ = h.Update(statusUpdateMsg{attachedSessionID: sel.ID})

	if strings.Contains(h.View(), stalePreAttachCapture) {
		t.Fatal("first frame after detach still renders the pre-attach preview capture")
	}
}

// Spec: the preview "for the selected session" is the one refetched. When the
// cursor sits on a different session than the one just left (e.g. switched via
// the notification bar), that selected session's preview is the one refreshed.
func TestAttachReturn_RefetchesTheSelectedNotOnlyTheAttachedSession(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	attached, selected := instances[0], instances[2]
	h.cursor = 2
	seedStalePreview(h, selected.ID)

	_, cmd := h.Update(statusUpdateMsg{attachedSessionID: attached.ID})

	h.previewCacheMu.RLock()
	got, ok := h.previewCache[selected.ID]
	h.previewCacheMu.RUnlock()
	if ok && got == stalePreAttachCapture {
		t.Error("selected session's pre-attach capture was not invalidated")
	}
	found := false
	for _, k := range collectPreviewFetchKeys(cmd) {
		if k == selected.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no preview fetch issued for the selected session %q", selected.ID)
	}
}
