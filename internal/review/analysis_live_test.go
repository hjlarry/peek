package review

import (
	"context"
	"os"
	"peek/internal/testutil"
	"strings"
	"testing"
	"time"
)

// Opt in explicitly: this uses the installed Codex CLI and its current login.
// Only the synthetic repository's contents are supplied to the model.
func TestAnalysisLiveCodex(t *testing.T) {
	if os.Getenv("PEEK_LIVE_ANALYSIS") != "1" {
		t.Skip("set PEEK_LIVE_ANALYSIS=1 to test feature grouping with real Codex")
	}
	repo := testutil.Repo(t)
	analysisWrite(t, repo, "main_test.go", "package main\n")
	snapshot, err := ReadSnapshot(repo)
	if err != nil || snapshot.ID == "" {
		t.Fatalf("read synthetic snapshot: %+v, %v", snapshot, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	started := time.Now()
	lastPhase := ""
	result, err := Analyze(ctx, repo, snapshot, func(phase, _ string) {
		if phase != lastPhase {
			t.Logf("%s after %s", phase, time.Since(started).Round(time.Millisecond))
			lastPhase = phase
		}
	})
	elapsed := time.Since(started).Round(time.Millisecond)
	if err != nil {
		t.Fatalf("real Codex grouping failed after %s: %v", elapsed, err)
	}
	if result.Snapshot != snapshot || strings.TrimSpace(result.Summary) == "" || len(result.Items) < 3 || result.Session == "" {
		t.Fatalf("real Codex returned incomplete or wrong-scope grouping after %s: %+v", elapsed, result)
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if item.ID == "" || item.Title == "" || item.Summary == "" || len(item.Files) == 0 {
			t.Fatalf("incomplete group: %+v", item)
		}
		for _, p := range item.Files {
			if seen[p] || analysisFixedGroup(p) != "" && analysisFixedGroup(p) != item.Title {
				t.Fatalf("duplicate or incorrectly fixed file: %s in %+v", p, item)
			}
			seen[p] = true
		}
	}
	t.Logf("real Codex passed in %s: %d groups, %d files; %s", elapsed, len(result.Items), len(seen), result.Summary)
}
