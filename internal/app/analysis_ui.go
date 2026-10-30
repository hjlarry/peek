package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"peek/internal/git"
	"peek/internal/review"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
)

type analysisState struct {
	run      *analysisRun
	selected int // -1 shows all changes
}

type analysisRun struct {
	codexTask
	snapshot review.Snapshot
	result   *review.Analysis
	err      string
	stale    bool
}

func (a *app) analysisRunning() bool {
	return a.analysis.run != nil && a.analysis.run.running
}

func (a *app) stopAnalyses() {
	if a.analysis.run != nil {
		a.analysis.run.stop()
	}
}

func (a *app) analysisPanel() {
	a.showNavigator(navAnalysis)
	a.showTree, a.collapsedTree = true, false
}

func (a *app) analysisMatches(run *analysisRun) bool {
	if run == nil || run.stale || run.result == nil {
		return false
	}
	_, id, _ := a.answerable()
	return id != "" && id == run.result.Snapshot.ID
}

func (a *app) startAnalysis() {
	if a.codexRunning() || a.changing() {
		return
	}
	cm, id, _ := a.answerable()
	if id == "" {
		return
	}
	a.analysisPanel()
	a.selectAnalysisItem(-1)
	snapshot := a.reviewSource
	if git.IsRange(cm.Hash) {
		snapshot = a.branchReview
	} else if cm.Hash != git.Worktree {
		snapshot = review.Snapshot{ID: id, Scope: review.CommitScope, Head: cm.Hash}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	run := &analysisRun{codexTask: codexTask{running: true, cancel: cancel, phase: "Reading changes…", started: time.Now()}, snapshot: snapshot}
	if previous := a.analysis.run; previous != nil {
		run.result = previous.result
	}
	a.analysis.run = run
	progress := a.codexProgress(ctx, &run.codexTask, func() bool { return a.analysis.run == run && !run.stale })
	repo := a.repo
	a.background(func() func() {
		defer cancel()
		var result review.Analysis
		var err error
		if snapshot.Scope == review.CommitScope {
			snapshot, err = review.CommitSnapshot(repo, cm.Hash)
		}
		if err == nil {
			result, err = review.Analyze(ctx, repo, snapshot, progress)
		}
		return func() {
			if a.closed || a.analysis.run != run {
				return
			}
			a.clearAnalysisFilter()
			run.finish()
			switch {
			case run.stale || errors.Is(err, review.ErrChanged):
				run.stale, run.err = true, "Changes have changed. Reanalyze to update the groups."
			case errors.Is(err, context.Canceled):
				run.err = "Analysis cancelled."
			case err != nil:
				run.err = err.Error()
			default:
				run.result = &result
			}
			if run.result != nil {
				run.snapshot = run.result.Snapshot
			}
			a.saveAnalyses()
		}
	})
}

func (a *app) invalidateAnalyses(branch bool, snapshot review.Snapshot, sourceErr string) {
	run := a.analysis.run
	if run == nil || run.snapshot.Scope == review.CommitScope || (run.snapshot.Scope == review.BranchScope) != branch {
		return
	}
	if run.snapshot.ID != snapshot.ID || sourceErr != "" {
		a.clearAnalysisFilter()
		run.stale = true
		run.stop()
	}
}

func (a *app) selectedAnalysisItem() *review.AnalysisItem {
	run, i := a.analysis.run, a.analysis.selected
	if !a.analysisMatches(run) || i < 0 || i >= len(run.result.Items) {
		return nil
	}
	return &run.result.Items[i]
}

// A background result must not move a different commit or the file the user is reading.
func (a *app) clearAnalysisFilter() {
	active := a.selectedAnalysisItem() != nil
	a.analysis.selected = -1
	if active {
		a.diffList = ui.ListState{}
		a.change = changeMark{}
		a.scrollTarget, a.settledFirst = -1, -1
		a.scrollToFile(a.file)
	}
}

func (a *app) selectAnalysisItem(index int) {
	run := a.analysis.run
	if index >= 0 && (!a.analysisMatches(run) || index >= len(run.result.Items)) {
		return
	}
	a.analysis.selected = index
	a.diffList = ui.ListState{}
	a.change = changeMark{}
	a.scrollTarget, a.settledFirst = -1, -1
	diffs := a.visibleDiffs()
	if index >= 0 {
		for _, d := range diffs {
			d.opened = true // Selecting Tests also reveals files folded by default.
		}
	}
	if len(diffs) > 0 {
		a.selectAnalysisFile(diffs[0].change.Path)
	}
}

func (a *app) analysisIncludes(p string) bool {
	item := a.selectedAnalysisItem()
	return item == nil || slices.Contains(item.Files, p)
}

func (a *app) visibleDiffs() []*fileDiff {
	item := a.selectedAnalysisItem()
	if item == nil {
		return a.diffs
	}
	var visible []*fileDiff
	for _, d := range a.diffs {
		if slices.Contains(item.Files, d.change.Path) {
			visible = append(visible, d)
		}
	}
	return visible
}

func (a *app) selectAnalysisFile(path string) {
	if d := a.diffOf[path]; d != nil && a.analysisIncludes(path) {
		d.opened = true
		a.file = path
		a.changeTree.show(path)
		a.scrollToFile(path)
	}
}

func (a *app) analysesFile() string {
	if a.convFile == "" {
		return ""
	}
	return a.convFile + ".features.json"
}

func (a *app) saveAnalyses() {
	file, run := a.analysesFile(), a.analysis.run
	if file == "" || run == nil || run.result == nil {
		return
	}
	data, err := json.Marshal(run.result)
	if err != nil {
		return
	}
	if err = os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
		tmp := file + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, file)
		}
	}
	if err != nil {
		run.err = fmt.Sprintf("Analysis completed, but could not be saved: %v", err)
	}
}

func (a *app) loadAnalyses() {
	data, err := os.ReadFile(a.analysesFile())
	if err != nil {
		return
	}
	var result review.Analysis
	if json.Unmarshal(data, &result) == nil && result.Snapshot.ID != "" && len(result.Items) > 0 {
		a.analysis.run = &analysisRun{snapshot: result.Snapshot, result: &result}
	}
}
