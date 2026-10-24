package ui

// Spec: docs/design/2026-10-23-ask-groups.md, section "Preview refresh after
// detach": "When the user detaches from a session back to the list, the
// preview for the selected session is refetched immediately (its cached
// capture invalidated and a fetch issued on the attach-return path), so the
// first frame after return shows current output rather than the pre-attach
// capture."
//
// Revised by "Other fixes from review": "Attach-return preview refresh keeps
// the old capture on screen until the new one lands (it expires the cache
// time instead of deleting the entry), and a fetch result is applied only if
// it belongs to the latest fetch for that session, so an in-flight pre-attach
// capture cannot overwrite the fresh one."
//
// Assumed: the fetch generation travels inside previewFetchedMsg (stamped
// when fetchPreview/refetchSelectedPreviewNow build the cmd), so a message
// obtained from an older cmd stays stale after its content is replaced here.
// "Expired" means previewCacheTime[key] is absent or older than the tick
// handler's 2s previewCacheTTL.

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

// previewExpired reports whether key's cache time is absent or past the 2s TTL.
func previewExpired(h *Home, key string) bool {
	h.previewCacheMu.RLock()
	defer h.previewCacheMu.RUnlock()
	at, ok := h.previewCacheTime[key]
	return !ok || time.Since(at) > 2*time.Second
}

func previewCached(h *Home, key string) (string, bool) {
	h.previewCacheMu.RLock()
	defer h.previewCacheMu.RUnlock()
	got, ok := h.previewCache[key]
	return got, ok
}

// Spec (revised): attach return "expires the cache time instead of deleting
// the entry". Checked before any returned Cmd runs, i.e. synchronously in
// Update.
func TestAttachReturn_InvalidatesSelectedPreviewCache(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	h.cursor = 0
	seedStalePreview(h, sel.ID)

	_, _ = h.Update(statusUpdateMsg{attachedSessionID: sel.ID})

	if !previewExpired(h, sel.ID) {
		t.Error("attach return left the selected preview's cache time fresh; it must be expired")
	}
	if got, ok := previewCached(h, sel.ID); !ok || got != stalePreAttachCapture {
		t.Errorf("attach return deleted the cached capture (got %q, present=%v); it must stay until the new one lands", got, ok)
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

// Spec (revised): "keeps the old capture on screen until the new one lands".
// The first frame after return still shows the cached capture rather than a
// blank or loading pane.
func TestAttachReturn_FirstFrameKeepsOldCaptureUntilNewLands(t *testing.T) {
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

	if !strings.Contains(h.View(), stalePreAttachCapture) {
		t.Fatal("first frame after detach dropped the old capture before the new one landed")
	}

	// The latest fetch lands. Taken from a direct refetch rather than by
	// running the attach-return Cmd, whose status reconcile would mutate sel.
	fresh := fetchedMsgFrom(t, h.refetchSelectedPreviewNow(), sel.ID)
	fresh.content, fresh.err = "FRESH-POST-ATTACH-CAPTURE", nil
	_, _ = h.Update(fresh)
	if v := h.View(); !strings.Contains(v, "FRESH-POST-ATTACH-CAPTURE") || strings.Contains(v, stalePreAttachCapture) {
		t.Fatal("after the new capture landed the preview does not show it in place of the old one")
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

	if !previewExpired(h, selected.ID) {
		t.Error("selected session's pre-attach capture was not expired")
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

// fetchedMsgFrom runs cmd and returns its previewFetchedMsg for key.
func fetchedMsgFrom(t *testing.T, cmd tea.Cmd, key string) previewFetchedMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil preview fetch cmd")
	}
	var found *previewFetchedMsg
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
			if m.previewKey == key && found == nil {
				mm := m
				found = &mm
			}
		}
	}
	walk(cmd())
	if found == nil {
		t.Fatalf("cmd issued no direct preview fetch for %q", key)
	}
	return *found
}

// Spec (revised): "a fetch result is applied only if it belongs to the latest
// fetch for that session, so an in-flight pre-attach capture cannot overwrite
// the fresh one." The pre-attach fetch's result arrives after the
// attach-return fetch's result.
func TestAttachReturn_StaleFetchResultArrivingLateIsIgnored(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	h.cursor = 0

	preAttach := fetchedMsgFrom(t, h.fetchPreview(sel, sel.ID, -1), sel.ID)
	postAttach := fetchedMsgFrom(t, h.refetchSelectedPreviewNow(), sel.ID)
	preAttach.content, preAttach.err = "OLD-INFLIGHT-CAPTURE", nil
	postAttach.content, postAttach.err = "FRESH-POST-ATTACH-CAPTURE", nil

	_, _ = h.Update(postAttach)
	_, _ = h.Update(preAttach)

	if got, _ := previewCached(h, sel.ID); got != "FRESH-POST-ATTACH-CAPTURE" {
		t.Fatalf("cached preview = %q after a stale result arrived late, want the fresh capture", got)
	}
}

// Guard for the same rule: results in issue order both apply, the newer last.
func TestAttachReturn_FetchResultsInOrderApplyNewest(t *testing.T) {
	h, instances := homeWithInstances(t, 3)
	sel := instances[0]
	h.cursor = 0

	preAttach := fetchedMsgFrom(t, h.fetchPreview(sel, sel.ID, -1), sel.ID)
	postAttach := fetchedMsgFrom(t, h.refetchSelectedPreviewNow(), sel.ID)
	preAttach.content, preAttach.err = "OLD-INFLIGHT-CAPTURE", nil
	postAttach.content, postAttach.err = "FRESH-POST-ATTACH-CAPTURE", nil

	_, _ = h.Update(preAttach)
	_, _ = h.Update(postAttach)

	if got, _ := previewCached(h, sel.ID); got != "FRESH-POST-ATTACH-CAPTURE" {
		t.Fatalf("cached preview = %q, want the newest capture", got)
	}
}
