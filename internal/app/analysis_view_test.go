package app

import (
	"os"
	"peek/internal/review"
	"peek/internal/testutil"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAnalysisPaneSelectsFeatureDirectly(t *testing.T) {
	a, _ := testApp(t, testutil.Repo(t), nil)
	run := &analysisRun{result: &review.Analysis{Snapshot: a.reviewSource, Summary: "增加程序入口，更新说明。", Items: []review.AnalysisItem{
		{ID: "entry", Title: "新增启动入口", Summary: "程序现在可以从 main 入口执行。", Files: []string{"main.go", "old.txt"}},
		{ID: "docs", Title: "文档", Summary: "添加使用说明。", Files: []string{"docs/new.md"}},
	}}}
	a.analysis.run = run
	a.analysisPanel()
	tt := ui.NewTester(a.analysisPane, 260, 800)
	if !tt.HasText("All Changes") || !tt.HasText("新增启动入口") || !tt.HasText("文档") {
		t.Fatal("feature list is incomplete", tt.Texts())
	}
	if tt.HasText(run.result.Summary) || tt.HasText(run.result.Items[0].Summary) {
		t.Fatal("sidebar duplicates the diff summary", tt.Texts())
	}
	if err := tt.Click("新增启动入口"); err != nil {
		t.Fatal(err)
	}
	if a.analysis.selected != 0 || !a.analysisIncludes("main.go") || a.analysisIncludes("docs/new.md") {
		t.Fatal("clicking a feature did not filter its files")
	}
	if len(a.visibleDiffs()) != 2 || !tt.HasText("main.go") || !tt.HasText("old.txt") {
		t.Fatal("selected feature did not expose its code")
	}
	if err := tt.Click("old.txt"); err != nil {
		t.Fatal(err)
	}
	if a.file != "old.txt" || a.analysis.selected != 0 {
		t.Fatal("file link lost its group selection", a.file, a.analysis.selected)
	}
	if out := os.Getenv("PEEK_ANALYSIS_OUT"); out != "" {
		whole := ui.NewTester(a.view, 1400, 900)
		settle(whole)
		save(t, out, whole.Image())
	}
	if err := tt.Click("文档"); err != nil {
		t.Fatal(err)
	}
	if a.analysis.selected != 1 || len(a.visibleDiffs()) != 1 || !a.analysisIncludes("docs/new.md") {
		t.Fatal("switching groups did not replace the visible files")
	}
	if err := tt.Click("All Changes"); err != nil {
		t.Fatal(err)
	}
	if a.analysis.selected != -1 || len(a.visibleDiffs()) != 3 || !a.analysisIncludes("main.go") {
		t.Fatal("all changes did not restore every file")
	}
}

func TestAnalysisPaneDisablesOutdatedGroups(t *testing.T) {
	a, _ := testApp(t, testutil.Repo(t), nil)
	run := &analysisRun{result: &review.Analysis{Snapshot: a.reviewSource,
		Items: []review.AnalysisItem{{Title: "功能修改", Files: []string{"main.go"}}}}}
	a.analysis.run = run
	a.analysisPanel()
	tt := ui.NewTester(a.analysisPane, 260, 800)
	for _, stale := range []bool{false, true} {
		run.stale = stale
		run.result.Snapshot.ID = "another-snapshot"
		a.analysis.selected = -1
		tt.Frame()
		want := "These changes have not been analyzed."
		if stale {
			want = "Changes have changed. Reanalyze to update the groups."
		}
		if !tt.HasText(want) {
			t.Fatalf("missing stale/scope notice: %q", tt.Texts())
		}
		_ = tt.Click("功能修改")
		if a.analysis.selected != -1 || len(a.visibleDiffs()) != 3 || tt.HasText("main.go") {
			t.Fatal("an obsolete group filtered or exposed links to current code")
		}
	}
}

func TestAnalysisPaneProgressCancellationAndError(t *testing.T) {
	a := newApp(t.TempDir())
	tt := ui.NewTester(a.analysisPane, 260, 700)
	if !tt.HasText("Analyze Features") {
		t.Fatal("empty state has no start control", tt.Texts())
	}
	cancelled := false
	run := &analysisRun{codexTask: codexTask{running: true, phase: "Grouping changes…", cancel: func() { cancelled = true }}}
	a.analysis.run = run
	tt.Frame()
	if !tt.HasText(run.phase) || tt.HasText("Analyze Features") {
		t.Fatal("analysis progress is missing", tt.Texts())
	}
	if err := tt.Click("Cancel Analysis"); err != nil || !cancelled {
		t.Fatalf("analysis did not cancel: %v", err)
	}
	run.finish()
	run.err = "Analysis cancelled."
	tt.Frame()
	if !tt.HasText(run.err) || !tt.HasText("Reanalyze") || tt.HasText("Cancel Analysis") {
		t.Fatal("finished analysis retained progress or lost retry", tt.Texts())
	}
}
