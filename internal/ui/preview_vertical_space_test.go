package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func setPreviewCapture(h *Home, id, content string) {
	h.previewCacheMu.Lock()
	h.previewCache[id] = content
	h.previewCacheMu.Unlock()
}

func maxBlankRun(rows []string) int {
	run, best := 0, 0
	for _, r := range rows {
		if strings.TrimSpace(ansi.Strip(r)) == "" {
			run++
			if run > best {
				best = run
			}
		} else {
			run = 0
		}
	}
	return best
}

// Contract A: blank runs in the capture are collapsed before windowing, so the
// output fills the pane and does not end in a blank band.
func TestPreviewPane_BlankRunsDoNotWasteVerticalSpace(t *testing.T) {
	h, inst := previewScrollSessionWithLines(t, 120, 40, 1)

	var parts []string
	for i := 0; i < 30; i++ {
		parts = append(parts, fmt.Sprintf("content-%02d", i))
		parts = append(parts, "", "", "", "", "")
	}
	// Trailing ANSI-only "blank" lines must be dropped.
	parts = append(parts, "\x1b[39m", "\x1b[0m  ", "\x1b[39m")
	setPreviewCapture(h, inst.ID, strings.Join(parts, "\n"))

	const height = 24
	out := ansi.Strip(h.renderPreviewPane(120, height))
	rows := strings.Split(strings.TrimRight(out, "\n"), "\n")

	start := -1
	for i, r := range rows {
		if strings.Contains(r, "content-") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no captured content rendered:\n%s", out)
	}
	if got := maxBlankRun(rows[start:]); got > 2 {
		t.Fatalf("output section has a run of %d blank rows, want <= 2:\n%s", got, out)
	}
	last := len(rows) - 1
	for last >= 0 && strings.TrimSpace(rows[last]) == "" {
		last--
	}
	if !strings.Contains(rows[last], "content-29") {
		t.Fatalf("last non-blank row %q is not the last captured line:\n%s", rows[last], out)
	}
	if trailing := len(rows) - 1 - last; trailing > 0 {
		t.Fatalf("pane ends with %d blank rows while older output is hidden:\n%s", trailing, out)
	}
	// Control: a dense overflowing capture (no blank runs) shows how many rows
	// the pane fills when it is fully used. The blank-run capture must fill the
	// same number of rows.
	dense := make([]string, 60)
	for i := range dense {
		dense[i] = fmt.Sprintf("dense-%02d", i)
	}
	setPreviewCapture(h, inst.ID, strings.Join(dense, "\n"))
	denseOut := ansi.Strip(h.renderPreviewPane(120, height))
	denseRows := strings.Split(strings.TrimRight(denseOut, "\n"), "\n")
	if len(rows) != len(denseRows) {
		t.Fatalf("rendered %d rows with blank runs, want %d (the rows a dense capture fills); freed rows must show older output:\n%s", len(rows), len(denseRows), out)
	}
	if !strings.Contains(out, "more lines above") {
		t.Fatalf("fixture should overflow the pane (expected 'more lines above'):\n%s", out)
	}
}

// Contract B: the stacked layout keeps an exact height in both title modes and
// compact mode gives the preview one extra content row.
func TestStackedLayout_TitleHeightFollowsCompact(t *testing.T) {
	const total = 36
	var lines []string
	for i := 0; i < 80; i++ {
		lines = append(lines, fmt.Sprintf("row-%02d", i))
	}
	content := strings.Join(lines, "\n")

	measure := func(compact bool) (int, int) {
		h, inst := previewScrollSessionWithLines(t, 120, 40, 1)
		h.previewOrientation = PreviewOrientationBelow
		h.compact = compact
		setPreviewCapture(h, inst.ID, content)
		rowsOut := strings.Split(h.renderStackedLayout(total), "\n")
		titleIdx := -1
		for i, r := range rowsOut {
			if strings.Contains(ansi.Strip(r), "PREVIEW") {
				titleIdx = i
				break
			}
		}
		if titleIdx < 0 {
			t.Fatalf("no PREVIEW title in stacked output")
		}
		titleLines := 2
		if compact {
			titleLines = 1
		}
		return len(rowsOut), len(rowsOut) - titleIdx - titleLines
	}

	classicLines, classicPreview := measure(false)
	compactLines, compactPreview := measure(true)
	if classicLines != total {
		t.Fatalf("classic stacked height = %d, want %d", classicLines, total)
	}
	if compactLines != total {
		t.Fatalf("compact stacked height = %d, want %d", compactLines, total)
	}
	if compactPreview != classicPreview+1 {
		t.Fatalf("compact preview content rows = %d, classic = %d; want compact = classic+1", compactPreview, classicPreview)
	}
}
