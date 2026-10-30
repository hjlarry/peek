package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"peek/internal/testutil"
	"strings"
	"testing"
)

func fakePullCheckout(t *testing.T, baseName, baseOID, head string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	metadata, err := json.Marshal(map[string]string{"baseRefName": baseName, "baseRefOid": baseOID})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PEEK_TEST_PR_METADATA", string(metadata))
	t.Setenv("PEEK_TEST_PR_HEAD", head)
	t.Setenv("PEEK_TEST_PR_LOG", log)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$PEEK_TEST_PR_LOG"
case "$1 $2" in
  "pr view") printf '%s' "$PEEK_TEST_PR_METADATA" ;;
  "auth git-credential")
    cat >/dev/null
    printf 'username=peek-test\npassword=synthetic-token\n' ;;
  "pr checkout")
    if git show-ref --verify --quiet "refs/heads/pr-$3"; then
      git switch -q "pr-$3"
      git merge --ff-only --quiet "$PEEK_TEST_PR_HEAD"
    else
      git switch -q -c "pr-$3" "$PEEK_TEST_PR_HEAD"
    fi ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestCheckoutPullRequestUsesAuthoritativeBase(t *testing.T) {
	for _, baseName := range []string{"main", "release/next"} {
		t.Run(baseName, func(t *testing.T) {
			repo, remote := testutil.RemoteRepo(t)
			testutil.Git(t, repo, "reset", "--hard", "HEAD")
			testutil.Git(t, repo, "clean", "-fd")
			old := testutil.Git(t, repo, "rev-parse", "HEAD")
			commit := func(name string) string {
				if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				testutil.Git(t, repo, "add", "--all")
				testutil.Git(t, repo, "commit", "-qm", "Add "+name)
				return testutil.Git(t, repo, "rev-parse", "HEAD")
			}
			base := commit("upstream.go")
			testutil.Git(t, repo, "push", "-q", "origin", "HEAD:refs/heads/"+baseName)
			first := commit("feature-one.go")
			head := commit("feature-two.go")
			testutil.Git(t, repo, "switch", "-q", "-c", "start", old)
			testutil.Git(t, repo, "update-ref", "refs/heads/main", old)
			testutil.Git(t, repo, "update-ref", "refs/remotes/origin/main", old)
			if baseName != "main" {
				testutil.Git(t, repo, "update-ref", "-d", "refs/remotes/origin/"+baseName)
			}
			// The PR's base repository has no matching configured remote.
			const page = "https://github.com/upstream/project"
			testutil.Git(t, repo, "config", "url."+remote+".insteadOf", page+".git")
			fakePullCheckout(t, baseName, base, head)
			if err := CheckoutPullRequest(context.Background(), repo, page, 7); err != nil {
				t.Fatal(err)
			}
			ref := "refs/peek/pull/upstream/project/7/base"
			if got := testutil.Git(t, repo, "rev-parse", ref); got != base {
				t.Fatalf("recorded base %s, want %s", got, base)
			}
			if got := testutil.Git(t, repo, "rev-parse", "refs/heads/main"); got != old {
				t.Fatalf("PR checkout changed local main: %s", got)
			}
			assertRange := func() {
				t.Helper()
				r, ok := BranchBase(repo)
				if !ok || r.Base != base || r.Head != head || r.Onto != baseName || r.Commits != 2 {
					t.Fatalf("PR range %+v, %v; want base %s, head %s, onto %s, 2 commits", r, ok, base, head, baseName)
				}
			}
			// A tag of the same name must not shadow the local branch's config.
			testutil.Git(t, repo, "tag", "pr-7", old)
			assertRange()
			changes, err := LoadChanges(repo, Range(base, head))
			if err != nil || len(changes) != 2 || changes[0].Path != "feature-one.go" || changes[1].Path != "feature-two.go" {
				t.Fatalf("PR includes unrelated base changes: %+v, %v", changes, err)
			}
			// Local main ahead must not hide the PR's first feature commit.
			testutil.Git(t, repo, "update-ref", "refs/heads/main", first)
			assertRange()
			if baseName == "main" {
				// A fork can have the same branch name as the PR target.
				testutil.Git(t, repo, "branch", "-D", "main")
				testutil.Git(t, repo, "branch", "-m", "main")
				assertRange()
			}
		})
	}
}

func TestCheckoutPullRequestRefreshesBaseOnReopen(t *testing.T) {
	repo, remote := testutil.RemoteRepo(t)
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "First feature")
	first := testutil.Git(t, repo, "rev-parse", "HEAD")
	testutil.Git(t, repo, "commit", "--allow-empty", "-qm", "Second feature")
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	old := testutil.Git(t, repo, "rev-parse", "origin/main")
	testutil.Git(t, repo, "push", "-q", "origin", first+":refs/heads/stacked-parent")
	const page = "https://github.com/upstream/project"
	testutil.Git(t, repo, "config", "url."+remote+".insteadOf", page+".git")
	fakePullCheckout(t, "main", old, head)
	if err := CheckoutPullRequest(context.Background(), repo, page, 7); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]string{"baseRefName": "stacked-parent", "baseRefOid": first})
	t.Setenv("PEEK_TEST_PR_METADATA", string(metadata))
	if err := CheckoutPullRequest(context.Background(), repo, page, 7); err != nil {
		t.Fatal(err)
	}
	if r, ok := BranchBase(repo); !ok || r.Base != first || r.Onto != "stacked-parent" || r.Commits != 1 {
		t.Fatalf("reopened PR kept its former base: %+v, %v", r, ok)
	}
}

func TestCheckoutPullRequestBaseFailureDoesNotCheckout(t *testing.T) {
	for _, failure := range []string{"invalid metadata", "fetch failure"} {
		t.Run(failure, func(t *testing.T) {
			repo := testutil.Repo(t)
			head := testutil.Git(t, repo, "rev-parse", "HEAD")
			log := fakePullCheckout(t, "main", head, head)
			const page = "https://github.com/upstream/project"
			if failure == "invalid metadata" {
				t.Setenv("PEEK_TEST_PR_METADATA", `{"baseRefName":"main","baseRefOid":"--invalid"}`)
			} else {
				t.Setenv("PEEK_TEST_PR_METADATA", `{"baseRefName":"main","baseRefOid":"1111111111111111111111111111111111111111"}`)
				testutil.Git(t, repo, "config", "url."+filepath.Join(t.TempDir(), "missing.git")+".insteadOf", page+".git")
			}
			if err := CheckoutPullRequest(context.Background(), repo, page, 7); err == nil || !strings.Contains(err.Error(), "base") {
				t.Fatalf("missing clear base failure: %v", err)
			}
			data, err := os.ReadFile(log)
			if err != nil || strings.Contains(string(data), "pr checkout") || CurrentBranch(repo) != "main" {
				t.Fatalf("changed branches after failed base preparation: %s, %v", data, err)
			}
			if out, err := Run(context.Background(), repo, "config", "--get-regexp", `^branch\..*\.peek-pr-base-`); err == nil || out != "" {
				t.Fatalf("failed checkout persisted a PR base association: %q, %v", out, err)
			}
		})
	}
}

func TestRecordedPullBaseNeverFallsBack(t *testing.T) {
	repo := testutil.Repo(t)
	base := testutil.Git(t, repo, "rev-parse", "HEAD")
	testutil.Git(t, repo, "switch", "-q", "-c", "feature")
	testutil.Git(t, repo, "add", "--all")
	testutil.Git(t, repo, "commit", "-qm", "Feature")
	const ref = "refs/peek/pull/upstream/project/7/base"
	for _, missing := range []string{"name", "ref key", "ref object"} {
		t.Run(missing, func(t *testing.T) {
			testutil.Git(t, repo, "update-ref", ref, base)
			testutil.Git(t, repo, "config", "branch.feature.peek-pr-base-ref", ref)
			testutil.Git(t, repo, "config", "branch.feature.peek-pr-base-name", "main")
			switch missing {
			case "name":
				testutil.Git(t, repo, "config", "--unset", "branch.feature.peek-pr-base-name")
			case "ref key":
				testutil.Git(t, repo, "config", "--unset", "branch.feature.peek-pr-base-ref")
			case "ref object":
				testutil.Git(t, repo, "update-ref", "-d", ref)
			}
			if r, ok := BranchBase(repo); ok {
				t.Fatalf("broken PR base fell back to main: %+v", r)
			}
		})
	}
}

func TestCheckoutPullRequestUsesScopedGitHubCredentials(t *testing.T) {
	repo, remote := testutil.RemoteRepo(t)
	head := testutil.Git(t, repo, "rev-parse", "HEAD")
	const page = "https://github.com/upstream/project"
	testutil.Git(t, repo, "config", "url."+remote+".insteadOf", page+".git")
	const staleHelper = "!f() { printf 'username=stale\\npassword=stale\\n'; }; f"
	testutil.Git(t, repo, "config", "credential.https://github.com.helper", staleHelper)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fakePullCheckout(t, "main", head, head)
	ghBin, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "CLI's folder")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ghBin, filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	credentials := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("PEEK_TEST_REAL_GIT", realGit)
	t.Setenv("PEEK_TEST_CREDENTIALS", credentials)
	// Exercise the fetch's actual per-command credential configuration offline.
	wrapper := `#!/bin/sh
if [ "$3" = "-c" ] && [ "$7" = "fetch" ]; then
  printf 'protocol=https\nhost=github.com\n\n' |
    "$PEEK_TEST_REAL_GIT" "$1" "$2" "$3" "$4" "$5" "$6" credential fill > "$PEEK_TEST_CREDENTIALS" || exit 1
fi
exec "$PEEK_TEST_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := CheckoutPullRequest(context.Background(), repo, page, 7); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(credentials)
	if err != nil || !strings.Contains(string(got), "username=peek-test\npassword=synthetic-token") {
		t.Fatalf("fetch did not use gh's login: %q, %v", got, err)
	}
	if got := testutil.Git(t, repo, "config", "--local", "--get-all", "credential.https://github.com.helper"); got != staleHelper {
		t.Fatalf("fetch changed saved credential helpers: %q", got)
	}
}
