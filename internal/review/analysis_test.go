package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"peek/internal/git"
	"peek/internal/testutil"
	"slices"
	"strings"
	"testing"
)

func analysisWrite(t *testing.T, repo, path, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, path), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func analysisSnapshot(t *testing.T, repo string) Snapshot {
	t.Helper()
	s, err := ReadSnapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func analysisResponse(t *testing.T, changes []analysisChange) []byte {
	t.Helper()
	var items []map[string]any
	for _, ch := range changes {
		if ch.FixedGroup == "" {
			items = append(items, map[string]any{"title": "Feature " + ch.Path, "summary": "Adjust feature behavior.", "file_ids": []string{ch.ID}})
		}
	}
	data, err := json.Marshal(map[string]any{"summary": "Feature updates.", "items": items})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAnalysisInventory(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.Git(t, repo, "reset", "--hard", "HEAD")
	testutil.Git(t, repo, "clean", "-fd")
	var lines []string
	for i := range 40 {
		lines = append(lines, fmt.Sprintf("line %d", i+1))
	}
	analysisWrite(t, repo, "code.txt", strings.Join(lines, "\n")+"\n")
	analysisWrite(t, repo, "rename.txt", "old name\n")
	analysisWrite(t, repo, "mode.txt", "mode\n")
	analysisWrite(t, repo, "binary.dat", "\x00old")
	analysisWrite(t, repo, ":(glob)*.txt", "literal\n")
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Fixture")
	lines[1], lines[36] = "changed first", "changed last"
	analysisWrite(t, repo, "code.txt", strings.Join(lines, "\n")+"\n")
	testutil.Git(t, repo, "mv", "rename.txt", "renamed.txt")
	if err := os.Chmod(filepath.Join(repo, "mode.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "config", "core.filemode", "true")
	analysisWrite(t, repo, "binary.dat", "\x00new")
	analysisWrite(t, repo, ":(glob)*.txt", "literal change\n")
	analysisWrite(t, repo, "new.txt", "new\nsecond\n")
	analysisWrite(t, repo, "empty.txt", "")
	if err := os.Remove(filepath.Join(repo, "old.txt")); err != nil {
		t.Fatal(err)
	}
	changes, err := analysisInventory(context.Background(), repo, analysisSnapshot(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]analysisChange{}
	for _, ch := range changes {
		if _, exists := byPath[ch.Path]; exists {
			t.Fatalf("file appears more than once: %s", ch.Path)
		}
		byPath[ch.Path] = ch
	}
	if strings.Count(byPath["code.txt"].Patch, "@@ -") != 2 || !strings.Contains(byPath[":(glob)*.txt"].Patch, "literal change") {
		t.Fatalf("hunks or literal path lost: %+v", byPath)
	}
	for _, path := range []string{"binary.dat", "renamed.txt", "mode.txt", "empty.txt", "old.txt", "new.txt"} {
		if ch := byPath[path]; ch.ID == "" || ch.Patch == "" {
			t.Fatalf("missing file change %q: %+v", path, ch)
		}
	}
	if byPath["renamed.txt"].OldPath != "rename.txt" || byPath["renamed.txt"].Status != "R" {
		t.Fatalf("rename metadata lost: %+v", byPath["renamed.txt"])
	}
	if byPath["old.txt"].Status != "D" || byPath["new.txt"].Status != "A" {
		t.Fatalf("wrong file status: %+v", byPath)
	}
}

func TestAnalyzeExactCommit(t *testing.T) {
	repo := testutil.Repo(t)
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	snapshot, err := CommitSnapshot(repo, head)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := analysisInventory(context.Background(), repo, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	testutil.FakeReviewCodex(t, string(analysisResponse(t, changes)))
	promptFile := filepath.Join(t.TempDir(), "prompt")
	t.Setenv("PEEK_TEST_REVIEW_PROMPT", promptFile)
	t.Setenv("PEEK_TEST_CODEX_THREAD", "analysis-thread")
	result, err := Analyze(context.Background(), repo, snapshot, func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot != snapshot || result.Session != "analysis-thread" {
		t.Fatalf("wrong source/session: %+v", result)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "Base: "+snapshot.Base+"\nHead: "+head) || strings.Contains(string(prompt), "docs/new.md") || !strings.Contains(string(prompt), "Write all titles and summaries in English.") {
		t.Fatalf("analysis did not retain exact commit: %s", prompt)
	}
	// Later commits and worktree edits do not change a historical selection.
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Later")
	if err := CheckAnalysisSnapshot(repo, snapshot); err != nil {
		t.Fatalf("historical selection became stale: %v", err)
	}
}

func TestAnalysisBranchSnapshot(t *testing.T) {
	repo, _ := testutil.RemoteRepo(t)
	testutil.Git(t, repo, "switch", "-c", "analysis-branch")
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Branch changes")
	snapshot, err := ReadBranchSnapshot(repo)
	if err != nil || snapshot.ID == "" {
		t.Fatalf("branch snapshot %+v, %v", snapshot, err)
	}
	changes, err := analysisInventory(context.Background(), repo, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	testutil.FakeReviewCodex(t, string(analysisResponse(t, changes)))
	if _, err := Analyze(context.Background(), repo, snapshot, nil); err != nil {
		t.Fatal(err)
	}
	analysisWrite(t, repo, "later.txt", "later\n")
	if err := CheckAnalysisSnapshot(repo, snapshot); err != nil {
		t.Fatalf("worktree edit invalidated fixed branch: %v", err)
	}
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Move branch")
	if err := CheckAnalysisSnapshot(repo, snapshot); !errors.Is(err, ErrChanged) {
		t.Fatalf("accepted moved branch: %v", err)
	}
}

func TestAnalysisRejectsIncompleteOrInventedMappings(t *testing.T) {
	changes := []analysisChange{
		{ID: "file-0001", Path: "one.go"},
		{ID: "file-0002", Path: "two.go"},
	}
	valid := analysisResponse(t, changes)
	for _, tc := range []struct{ name, data string }{
		{"unknown ID", strings.Replace(string(valid), "file-0001", "file-9999", 1)},
		{"duplicate across groups", strings.Replace(string(valid), "file-0002", "file-0001", 1)},
		{"fabricated paths", strings.Replace(string(valid), `"file_ids":`, `"files":["fake.go"],"file_ids":`, 1)},
		{"duplicate in group", strings.Replace(string(valid), `["file-0001"]`, `["file-0001","file-0001"]`, 1)},
		{"empty summary", strings.Replace(string(valid), `"summary":"Adjust feature behavior."`, `"summary":""`, 1)},
		{"empty title", strings.Replace(string(valid), `"title":"Feature one.go"`, `"title":""`, 1)},
		{"missing coverage", `{"summary":"Missing one file","items":[{"title":"Feature","summary":"Description.","file_ids":["file-0001"]}]}`},
		{"trailing JSON", string(valid) + `{}`},
		{"trailing array delimiter", string(valid) + `]`},
		{"trailing object delimiter", string(valid) + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readAnalysis([]byte(tc.data), changes); err == nil {
				t.Fatalf("accepted invalid analysis: %s", tc.data)
			}
		})
	}
	data := []byte(`{"summary":"Cross-file changes","items":[{"title":"Feature","summary":"Description.","file_ids":["file-0002","file-0001"]}]}`)
	if result, err := readAnalysis(data, changes); err != nil || !slices.Equal(result.Items[0].Files, []string{"two.go", "one.go"}) {
		t.Fatalf("valid cross-file group: %+v, %v", result, err)
	}
}

func TestAnalysisFixedGroups(t *testing.T) {
	for _, tc := range []struct{ path, group string }{
		{"main_test.go", "Tests"}, {"test_retry.py", "Tests"}, {"retry.spec.ts", "Tests"},
		{"Button.stories.tsx", "Tests"}, {"__tests__/data.json", "Tests"},
		{"fixtures/README.md", "Tests"}, {"__fixtures__/data.json", "Tests"},
		{"mocks/client.ts", "Tests"}, {"__mocks__/client.ts", "Tests"},
		{"__snapshots__/render.html", "Tests"}, {"snapshots/page.png", "Tests"},
		{"component.snap", "Tests"}, {"tests/query.generated.go", "Tests"},
		{"README.md", "Docs"}, {"README.txt", "Docs"}, {"LICENSE.txt", "Docs"},
		{"docs/config.json", "Docs"}, {"docs/notes.txt", "Docs"},
		{"pnpm-lock.yaml", "Generated"}, {"go.sum", "Generated"}, {"query.generated.go", "Generated"},
		{"api/types.gen.ts", "Generated"}, {"contracts/generated/client.ts", "Generated"},
		{"__generated__/schema.ts", "Generated"}, {"tests/generated/fixture.ts", "Tests"},
		{"package.json", ""}, {"go.mod", ""}, {"vite.config.ts", ""},
		{".storybook/main.ts", ""}, {"src/fixtures.ts", ""},
		{"requirements.txt", ""}, {"CMakeLists.txt", ""}, {"prompts/system.txt", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := analysisFixedGroup(tc.path); got != tc.group {
				t.Fatalf("group %q, want %q", got, tc.group)
			}
		})
	}
}

func TestAnalyzeKeepsFixedGroupsSeparate(t *testing.T) {
	repo := testutil.Repo(t)
	analysisWrite(t, repo, "main_test.go", "package main\n")
	analysisWrite(t, repo, "go.sum", "example v1 hash\n")
	snapshot := analysisSnapshot(t, repo)
	changes, err := analysisInventory(context.Background(), repo, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	testutil.FakeReviewCodex(t, string(analysisResponse(t, changes)))
	promptFile := filepath.Join(t.TempDir(), "prompt")
	t.Setenv("PEEK_TEST_REVIEW_PROMPT", promptFile)
	result, err := Analyze(context.Background(), repo, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 5 || result.Items[2].Title != "Tests" || result.Items[3].Title != "Docs" || result.Items[4].Title != "Generated" {
		t.Fatalf("fixed groups missing or reordered: %+v", result.Items)
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		for _, p := range item.Files {
			if seen[p] || analysisFixedGroup(p) != "" && analysisFixedGroup(p) != item.Title {
				t.Fatalf("file duplicated or in wrong group: %s in %+v", p, item)
			}
			seen[p] = true
		}
	}
	if len(seen) != len(changes) {
		t.Fatalf("files omitted: %v", seen)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil || !strings.Contains(string(prompt), `"fixed_group":"Tests"`) || !strings.Contains(string(prompt), `"path":"go.sum"`) {
		t.Fatalf("fixed context absent: %s, %v", prompt, err)
	}
}

func TestAnalyzeOmitsLargeGeneratedBodies(t *testing.T) {
	repo := testutil.Repo(t)
	analysisWrite(t, repo, "pnpm-lock.yaml", "old lockfile\n")
	testutil.Git(t, repo, "add", "pnpm-lock.yaml")
	testutil.Git(t, repo, "commit", "-qm", "Add lockfile")
	large := strings.Repeat("large-generated-body\n", maxAnalysisBytes/20+1)
	analysisWrite(t, repo, "pnpm-lock.yaml", large)
	analysisWrite(t, repo, "yarn.lock", large)
	snapshot := analysisSnapshot(t, repo)
	changes, err := analysisInventory(context.Background(), repo, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range changes {
		if ch.Path == "pnpm-lock.yaml" || ch.Path == "yarn.lock" {
			if ch.Patch != generatedAnalysisPatch || ch.FixedGroup != "Generated" {
				t.Fatalf("generated body retained: %+v", ch)
			}
		}
	}
	testutil.FakeReviewCodex(t, string(analysisResponse(t, changes)))
	result, err := Analyze(context.Background(), repo, snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := result.Items[len(result.Items)-1]
	if last.Title != "Generated" || !slices.Equal(last.Files, []string{"pnpm-lock.yaml", "yarn.lock"}) {
		t.Fatalf("large generated files missing: %+v", result.Items)
	}
	analysisWrite(t, repo, "yarn.lock", large+"changed again\n")
	if err := CheckAnalysisSnapshot(repo, snapshot); !errors.Is(err, ErrChanged) {
		t.Fatalf("omitted generated body lost snapshot protection: %v", err)
	}
}

func TestAnalyzeFixedOnlyDoesNotCallCodex(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.Git(t, repo, "reset", "--hard", "HEAD")
	testutil.Git(t, repo, "clean", "-fd")
	analysisWrite(t, repo, "README.md", "# Documentation\n")
	analysisWrite(t, repo, "main_test.go", "package main\n")
	testutil.FakeReviewCodex(t, "this must not be decoded")
	result, err := Analyze(context.Background(), repo, analysisSnapshot(t, repo), nil)
	if err != nil || result.Session != "" || len(result.Items) != 2 || result.Items[0].Title != "Tests" || result.Items[1].Title != "Docs" {
		t.Fatalf("fixed-only analysis: %+v, %v", result, err)
	}
}

func TestAnalyzeRejectsWorktreeChangesDuringCodex(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelTurn), func(t *testing.T) {
			repo := testutil.Repo(t)
			snapshot := analysisSnapshot(t, repo)
			changes, err := analysisInventory(context.Background(), repo, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			testutil.FakeReviewCodex(t, string(analysisResponse(t, changes)))
			dir := t.TempDir()
			events, ack := filepath.Join(dir, "events"), filepath.Join(dir, "ack")
			analysisWrite(t, dir, "events", "{\"type\":\"item.started\",\"item\":{\"type\":\"command_execution\",\"command\":\"git diff\"}}\n")
			t.Setenv("PEEK_TEST_CODEX_EVENTS", events)
			t.Setenv("PEEK_TEST_CODEX_ACK", ack)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_, err = Analyze(ctx, repo, snapshot, func(phase, _ string) {
				if phase == "Inspecting…" {
					if cancelTurn {
						cancel()
					} else {
						analysisWrite(t, repo, "main.go", "package different\n")
					}
					analysisWrite(t, dir, "ack", "ok")
				}
			})
			want := ErrChanged
			if cancelTurn {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

func TestAnalysisUnbornRepository(t *testing.T) {
	repo := t.TempDir()
	testutil.Git(t, repo, "init", "-q", "-b", "main")
	analysisWrite(t, repo, "staged.go", "staged\n")
	analysisWrite(t, repo, "untracked.go", "untracked\n")
	testutil.Git(t, repo, "add", "staged.go")
	snapshot := analysisSnapshot(t, repo)
	changes, err := analysisInventory(context.Background(), repo, snapshot)
	if err != nil || len(changes) != 2 {
		t.Fatalf("unborn inventory %+v, %v", changes, err)
	}
	if _, err := readAnalysis(analysisResponse(t, changes), changes); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := git.BoundedContext(ctx, repo, 100, "status"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Git did not honor cancellation: %v", err)
	}
}

func TestAnalysisRejectsMixedFileDirectoryPatch(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.Git(t, repo, "reset", "--hard", "HEAD")
	testutil.Git(t, repo, "clean", "-fd")
	if err := os.Remove(filepath.Join(repo, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "old.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	analysisWrite(t, repo, "old.txt/child.txt", "new child\n")
	testutil.Git(t, repo, "add", "--all")
	if _, err := analysisInventory(context.Background(), repo, analysisSnapshot(t, repo)); err == nil || !strings.Contains(err.Error(), "cannot map") {
		t.Fatalf("accepted descendant hunks as deleted-file lines: %v", err)
	}
}

func TestAnalysisRejectsTrackedAndUntrackedSamePath(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.Git(t, repo, "rm", "--cached", "main.go")
	if _, err := analysisInventory(context.Background(), repo, analysisSnapshot(t, repo)); err == nil || !strings.Contains(err.Error(), "separate tracked and untracked changes") {
		t.Fatalf("accepted two file IDs for a single path: %v", err)
	}
}
