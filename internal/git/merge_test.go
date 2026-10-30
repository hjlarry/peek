package git

import (
	"os"
	"path/filepath"
	"peek/internal/testutil"
	"strings"
	"testing"
)

func TestMergeCommitUsesFirstParent(t *testing.T) {
	repo := testutil.Repo(t)
	testutil.Git(t, repo, "reset", "--hard", "HEAD")
	testutil.Git(t, repo, "clean", "-fd")
	testutil.Git(t, repo, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Feature")
	testutil.Git(t, repo, "switch", "main")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n// unrelated main change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Main")
	testutil.Git(t, repo, "merge", "--no-ff", "-m", "Merge", "feature")
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	changes, err := LoadChanges(repo, head)
	if err != nil || len(changes) != 1 || changes[0].Path != "feature.txt" || changes[0].Status != "A" {
		t.Fatalf("first-parent merge changes %+v, %v", changes, err)
	}
	before, after, err := FileTextsAtMost(repo, head, changes[0], 1024)
	if err != nil || before != "" || after != "feature\n" {
		t.Fatalf("merge texts %q -> %q, %v", before, after, err)
	}
	patch, err := Patch(repo, head, changes[0], 4096)
	if err != nil || !strings.Contains(patch, "+feature") || strings.Contains(patch, "unrelated main") {
		t.Fatalf("merge patch %q, %v", patch, err)
	}
}
