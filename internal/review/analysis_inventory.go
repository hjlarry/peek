package review

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"peek/internal/git"
	"strings"
	"unicode/utf8"
)

type analysisChange struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	OldPath    string `json:"old_path"`
	Status     string `json:"status"`
	FixedGroup string `json:"fixed_group,omitempty"`
	Patch      string `json:"patch"`
}

const generatedAnalysisPatch = "Generated file or lockfile: diff body omitted; assigned to the fixed generated-files group.\n"

func analysisInventory(ctx context.Context, repo string, snapshot Snapshot) ([]analysisChange, error) {
	base, head := snapshot.Base, snapshot.Head
	if base == "" {
		base, head = head, ""
		if base == "" {
			var err error
			base, err = git.BoundedContext(ctx, repo, 128, "hash-object", "-t", "tree", "/dev/null")
			if err != nil {
				return nil, err
			}
			base = strings.TrimSpace(base)
		}
	}
	args := []string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none", "--no-color", "--find-renames", "--src-prefix=a/", "--dst-prefix=b/", base}
	if head != "" {
		args = append(args, head)
	}
	listed, err := git.BoundedContext(ctx, repo, maxMetadata, appendCopy(args, "--name-status", "-z", "--")...)
	if err != nil {
		return nil, err
	}
	files, err := analysisFiles(listed)
	if err != nil {
		return nil, err
	}
	var untracked []string
	if head == "" {
		listed, err := git.BoundedContext(ctx, repo, maxMetadata, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return nil, err
		}
		if listed != "" {
			untracked = strings.Split(strings.TrimSuffix(listed, "\x00"), "\x00")
		}
	}
	var inventory []analysisChange
	remaining := maxAnalysisBytes
	seenPaths := make(map[string]bool)
	add := func(ch git.Change, patch string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seenPaths[ch.Path] {
			return fmt.Errorf("cannot analyze separate tracked and untracked changes for %q; stage or unstage the replacement first", ch.Path)
		}
		seenPaths[ch.Path] = true
		remaining -= len(patch)
		if remaining < 0 {
			return fmt.Errorf("the complete change exceeds the 2 MiB analysis limit; select a smaller commit")
		}
		if patch == "" {
			return fmt.Errorf("no diff found for changed file %q", ch.Path)
		}
		// A literal pathspec still includes descendants when a file becomes a
		// directory. Do not attach those other files' changes to this file.
		if strings.Contains(patch, "\ndiff --git ") {
			return fmt.Errorf("cannot map the combined file/type change for %q safely; inspect its diff directly", ch.Path)
		}
		if !utf8.ValidString(ch.Path) {
			return fmt.Errorf("analysis requires UTF-8 file paths")
		}
		inventory = append(inventory, analysisChange{
			ID: fmt.Sprintf("file-%04d", len(inventory)+1), Path: ch.Path, OldPath: ch.Old,
			Status: ch.Status, FixedGroup: analysisFixedGroup(ch.Path), Patch: patch,
		})
		return nil
	}
	for _, ch := range files {
		if analysisFixedGroup(ch.Path) == "Generated" {
			if err := add(ch, generatedAnalysisPatch); err != nil {
				return nil, err
			}
			continue
		}
		paths := []string{ch.Path}
		if ch.Old != ch.Path {
			paths = append(paths, ch.Old)
		}
		patch, err := git.BoundedContext(ctx, repo, remaining, appendCopy(args, append([]string{"--unified=3", "--"}, paths...)...)...)
		if err != nil {
			return nil, fmt.Errorf("cannot read the complete change for %q: %w", ch.Path, err)
		}
		if err := add(ch, patch); err != nil {
			return nil, err
		}
	}
	for _, path := range untracked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if analysisFixedGroup(path) == "Generated" {
			if err := add(git.Change{Status: "A", Path: path, Old: path}, generatedAnalysisPatch); err != nil {
				return nil, err
			}
			continue
		}
		patch, err := untrackedAnalysisPatch(repo, path, remaining)
		if err != nil {
			return nil, err
		}
		if err := add(git.Change{Status: "A", Path: path, Old: path}, patch); err != nil {
			return nil, err
		}
	}
	if len(inventory) == 0 {
		return nil, fmt.Errorf("the selected scope has no net changes to analyze")
	}
	return inventory, nil
}

func appendCopy(args []string, more ...string) []string {
	return append(append([]string(nil), args...), more...)
}

// NUL-separated names preserve spaces, tabs and newlines in repository paths.
func analysisFiles(listed string) ([]git.Change, error) {
	parts := strings.Split(strings.TrimSuffix(listed, "\x00"), "\x00")
	var files []git.Change
	for i := 0; i < len(parts) && parts[i] != ""; {
		status := parts[i]
		if i+1 >= len(parts) || !strings.ContainsAny(status[:1], "AMDRCT") {
			return nil, fmt.Errorf("unsupported or unmerged change in analysis inventory")
		}
		ch := git.Change{Status: status[:1], Old: parts[i+1], Path: parts[i+1]}
		i += 2
		if ch.Status == "R" || ch.Status == "C" {
			if i >= len(parts) {
				return nil, fmt.Errorf("incomplete rename in analysis inventory")
			}
			ch.Path = parts[i]
			i++
		}
		if ch.Path == "" || !utf8.ValidString(ch.Path) || !utf8.ValidString(ch.Old) {
			return nil, fmt.Errorf("analysis requires nonempty UTF-8 file paths")
		}
		files = append(files, ch)
	}
	return files, nil
}

func untrackedAnalysisPatch(repo, path string, limit int) (string, error) {
	p := filepath.Join(repo, path)
	info, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		return fmt.Sprintf("New symbolic link %q -> %q\n", path, target), err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("cannot analyze nonregular untracked file %q", path)
	}
	if info.Size() > int64(limit) {
		return "", fmt.Errorf("untracked file %q exceeds the complete analysis size limit", path)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", fmt.Errorf("untracked file %q exceeds the complete analysis size limit", path)
	}
	if strings.ContainsRune(string(data), '\x00') || !utf8.Valid(data) {
		return fmt.Sprintf("New binary file %q (%d bytes); inspect the asset if necessary.\n", path, len(data)), nil
	}
	if len(data) == 0 {
		return fmt.Sprintf("New empty file %q (mode %s).\n", path, info.Mode()), nil
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	return fmt.Sprintf("New untracked file %q (mode %s)\n@@ -0,0 +1,%d @@\n+", path, info.Mode(), len(lines)) + strings.Join(lines, "\n+") + "\n", nil
}
