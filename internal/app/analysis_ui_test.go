package app

import (
	"os"
	"path/filepath"
	"peek/internal/review"
	"peek/internal/testutil"
	"slices"
	"strings"
	"testing"
)

func analysisTestResponse() string {
	return `{"summary":"新增程序入口并调整文档。","items":[{"title":"新增程序入口","summary":"程序可以从 main 函数启动。","file_ids":["file-0001"]},{"title":"清理旧内容","summary":"删除过时的内容文件。","file_ids":["file-0002"]}]}`
}

func TestAnalysisOneClickGroupsWholeDiff(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.FakeReviewCodex(t, analysisTestResponse())
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("PEEK_TEST_CODEX_ARGS", args)
	a, tt := testApp(t, repo, nil)
	if err := tt.Click("Feature Navigator"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	run := a.analysis.run
	if run == nil || run.result == nil || run.running || run.err != "" || len(run.result.Items) != 3 {
		t.Fatalf("one-click grouping failed: %+v", run)
	}
	if a.shownConversation() != nil || !a.analysisMatches(run) || a.navigator != navAnalysis {
		t.Fatal("grouping must stay beside the same diff")
	}
	for _, label := range []string{"解释代码变化", "解释功能变化", "代码解释", "功能梳理", "只看此功能"} {
		if tt.HasText(label) {
			t.Fatalf("obsolete control is still present: %s", label)
		}
	}
	runs, _ := os.ReadFile(args)
	if strings.Count(string(runs), "exec ") != 1 {
		t.Fatalf("expected one Codex request: %s", runs)
	}
	if err := tt.Click("新增程序入口"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if len(a.visibleDiffs()) != 1 || a.visibleDiffs()[0].change.Path != "main.go" || a.file != "main.go" {
		t.Fatal("clicking a feature did not show only its code")
	}
	if err := tt.Click("Docs"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if len(a.visibleDiffs()) != 1 || a.analysisIncludes("main.go") {
		t.Fatal("fixed docs group did not filter its files")
	}
	for _, d := range a.visibleDiffs() {
		if d.folded() {
			t.Fatal("selecting a category left its diff folded")
		}
	}
	if err := tt.Click("All Changes"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if len(a.visibleDiffs()) != 3 {
		t.Fatal("all changes did not restore the complete diff")
	}
	// Opening the saved categories does not make another model call.
	if err := tt.Click("File Navigator"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Feature Navigator"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	runs, _ = os.ReadFile(args)
	if strings.Count(string(runs), "exec ") != 1 {
		t.Fatal("reopening categories reran Codex")
	}
	a.selectAnalysisItem(0)
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// changed again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.refresh()
	settle(tt)
	if !run.stale || a.analysisMatches(run) || a.analysis.selected != -1 || len(a.visibleDiffs()) != len(a.diffs) {
		t.Fatal("changed worktree retained a stale filter")
	}
}

func TestAnalysisNavigatorTransitions(t *testing.T) {
	a, tt := testApp(t, testutil.Repo(t), nil)
	a.analysis.run = &analysisRun{snapshot: a.reviewSource, result: &review.Analysis{Snapshot: a.reviewSource,
		Items: []review.AnalysisItem{{Title: "Docs", Files: []string{"docs/new.md"}}}}}
	if err := tt.Click("Feature Navigator"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.navigator != navAnalysis || a.mode != modeChanges {
		t.Fatal("Features did not open its navigator")
	}
	a.selectAnalysisItem(0)
	if err := tt.Click("File Navigator"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.navigator != navFiles || a.analysis.selected != -1 || len(a.visibleDiffs()) != 3 || a.file != "docs/new.md" {
		t.Fatal("Files did not restore the full diff while preserving the current file")
	}
	if err := tt.Click("Feature Navigator"); err != nil {
		t.Fatal(err)
	}
	a.commitRow = 1
	settle(tt)
	if a.navigator != navAnalysis || !tt.HasText("These changes have not been analyzed.") {
		t.Fatal("choosing history lost the feature navigator or reused another scope's groups")
	}
	a.setMode(modeFiles)
	settle(tt)
	if a.navigator != navFiles {
		t.Fatal("Files mode retained the feature navigator")
	}
	if err := tt.Click("Feature Navigator"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.mode != modeChanges {
		t.Fatal("Features did not restore Changes mode")
	}
	a.filterFiles()
	settle(tt)
	if a.navigator != navFiles || !tt.HasText("Changed Files") {
		t.Fatal("Filter Files did not open the file navigator")
	}
}

func TestAnalysisNavigationStaysWithinSelectedGroup(t *testing.T) {
	a, tt := testApp(t, testutil.Repo(t), nil)
	a.analysis.run = &analysisRun{snapshot: a.reviewSource, result: &review.Analysis{Snapshot: a.reviewSource,
		Items: []review.AnalysisItem{{Title: "文档", Files: []string{"docs/new.md", "old.txt"}}, {Title: "入口", Files: []string{"main.go"}}}}}
	a.analysisPanel()
	a.selectAnalysisItem(0)
	settle(tt)
	for _, d := range a.visibleDiffs() {
		if d.state == nil {
			a.loadDiff(d)
		}
	}
	for range 8 {
		a.goChange(1)
		settle(tt)
		if !slices.Contains([]string{"docs/new.md", "old.txt"}, a.file) {
			t.Fatalf("next change escaped the selected group: %s", a.file)
		}
	}
	for range 8 {
		a.goChange(-1)
		settle(tt)
		if !a.analysisIncludes(a.file) {
			t.Fatalf("previous change escaped the selected group: %s", a.file)
		}
	}
	// Switching groups clears the previous hunk cursor and starts at the new file.
	a.selectAnalysisItem(1)
	settle(tt)
	a.goChange(1)
	if a.file != "main.go" || a.diffIndex("main.go") != 0 || a.diffIndex("old.txt") != -1 {
		t.Fatal("group switch retained the previous group's row indexes")
	}
}

func TestAnalysisFollowsFileWhenReturningToChanges(t *testing.T) {
	a, _ := testApp(t, testutil.Repo(t), nil)
	a.analysis.run = &analysisRun{snapshot: a.reviewSource, result: &review.Analysis{Snapshot: a.reviewSource,
		Items: []review.AnalysisItem{{Title: "入口", Files: []string{"main.go"}}}}}
	a.analysisPanel()
	a.selectAnalysisItem(0)
	a.mode, a.openFile = modeFiles, "main.go"
	a.setMode(modeChanges)
	if a.analysis.selected != 0 || a.file != "main.go" {
		t.Fatal("return to the selected group lost its filter")
	}
	a.mode, a.openFile = modeFiles, "docs/new.md"
	a.setMode(modeChanges)
	if a.analysis.selected != -1 || a.file != "docs/new.md" || len(a.visibleDiffs()) != 3 {
		t.Fatal("return from Files did not reveal the selected changed file")
	}
}

func TestAnalysisBackgroundResultDoesNotMoveAnotherCommit(t *testing.T) {
	testutil.FakeReviewCodex(t, analysisTestResponse())
	a, tt := testApp(t, testutil.Repo(t), nil)
	a.building = true
	a.startAnalysis()
	a.building = false
	pending := a.published
	a.published = nil
	a.commitRow = 1
	settle(tt)
	a.file = "old.txt"
	a.changeTree.show(a.file)
	a.scrollToFile(a.file)
	target := a.scrollTarget
	for _, publish := range pending {
		publish()
	}
	if a.file != "old.txt" || a.scrollTarget != target || a.analysisMatches(a.analysis.run) {
		t.Fatal("background grouping moved the historical diff being read")
	}
	a.invalidateAnalyses(false, review.Snapshot{ID: "new-worktree"}, "")
	if a.file != "old.txt" || a.scrollTarget != target {
		t.Fatal("invalidating another snapshot moved the current diff")
	}
}

func TestAnalysisSavedResultsStayBoundToSnapshot(t *testing.T) {
	repo := testutil.Repo(t)
	a, _ := testApp(t, repo, nil)
	a.convFile = filepath.Join(t.TempDir(), "conversations.json")
	result := &review.Analysis{Snapshot: a.reviewSource, Summary: "saved", Items: []review.AnalysisItem{{Title: "入口", Files: []string{"main.go"}}}}
	a.analysis.run = &analysisRun{snapshot: a.reviewSource, result: result}
	a.saveAnalyses()
	b, _ := testApp(t, repo, nil)
	b.convFile = a.convFile
	b.loadAnalyses()
	run := b.analysis.run
	if !b.analysisMatches(run) || run.result.Summary != "saved" {
		t.Fatal("same snapshot did not restore its groups")
	}
	b.selectAnalysisItem(0)
	b.change = changeMark{file: "main.go", hunk: 1, reveal: true}
	b.updateReviewSource(review.Snapshot{ID: "new-version", Scope: "Uncommitted changes"}, "")
	if !run.stale || b.analysisMatches(run) || b.analysis.selected != -1 || b.change.file != "" {
		t.Fatal("saved groups attached to another snapshot")
	}
}

func TestAnalysisFailedRetryKeepsPreviousGroups(t *testing.T) {
	testutil.FakeReviewCodex(t, analysisTestResponse())
	a, _ := testApp(t, testutil.Repo(t), nil)
	a.convFile = filepath.Join(t.TempDir(), "conversations.json")
	a.startAnalysis()
	previous := a.analysis.run.result
	if previous == nil {
		t.Fatal("initial analysis failed")
	}
	if err := os.WriteFile(os.Getenv("PEEK_TEST_REVIEW_RESPONSE"), []byte(`{"summary":"incomplete","items":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a.startAnalysis()
	run := a.analysis.run
	if run.result != previous || run.err == "" || !a.analysisMatches(run) {
		t.Fatalf("failed retry discarded valid groups: %+v", run)
	}
	b := newApp(a.repo)
	b.convFile = a.convFile
	b.loadAnalyses()
	if saved := b.analysis.run; saved == nil || saved.result.Summary != previous.Summary {
		t.Fatal("failed retry erased the saved groups")
	}
}

func TestAnalysisBlocksRepositoryMutations(t *testing.T) {
	a, _ := testApp(t, testutil.Repo(t), nil)
	a.analysis.run = &analysisRun{codexTask: codexTask{running: true}}
	a.canPush, a.behind = true, 1
	a.startPush()
	a.startPull()
	if a.push.running || a.pull.running {
		t.Fatal("repository mutation started while grouping reads the snapshot")
	}
}
