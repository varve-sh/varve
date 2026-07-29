// Package observer is ADR-0004's diff observer: the git plumbing that turns a
// commit into an ObservedCommit, and the walk that makes the record complete.
//
// It holds no attribution logic. Which decisions a commit touched, what the
// verdict is and what the verdict does to the lifecycle are the kernel's
// (internal/kernel/observe.go), so that half is testable without a repository
// and this half is testable without a decision store.
//
// Nothing here calls a model, touches the network, or blocks a commit
// (ADR-0004 §D7).
package observer

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/varve-sh/varve/internal/kernel"
)

// revertTrailer is §D2's rule, and the whole of revert detection for the 90
// days: the trailer `git revert` and the GitHub/GitLab revert buttons write.
// Patch-inverse detection is out — it needs a similarity threshold, and a
// judgement call in the verdict path is what ruling 1 exists to prevent.
var revertTrailer = regexp.MustCompile(`(?i)This reverts commit ([0-9a-f]{7,40})`)

// gitFieldSep separates the fields of the metadata read. Git emits it via the
// `%x1f` format escape (unit separator) rather than the byte appearing in the
// argument itself: exec arguments are NUL-terminated C strings, so a literal
// control byte in the format string fails the exec outright.
const (
	gitFieldEscape = "%x1f"
	gitFieldSep    = "\x1f"
)

// Show reads one commit into the shape the kernel observes (§D1.4).
func Show(repoRoot, sha string) (kernel.ObservedCommit, error) {
	var c kernel.ObservedCommit

	meta, err := gitOutput(repoRoot, "show", "-s",
		"--format=%H"+gitFieldEscape+"%an"+gitFieldEscape+"%s"+gitFieldEscape+"%cI"+gitFieldEscape+"%B", sha)
	if err != nil {
		return c, fmt.Errorf("reading commit %s: %w", sha, err)
	}
	parts := strings.SplitN(meta, gitFieldSep, 5)
	if len(parts) < 5 {
		return c, fmt.Errorf("unexpected git output for %s", sha)
	}

	committedAt, err := parseCommitTime(parts[3])
	if err != nil {
		return c, err
	}

	c = kernel.ObservedCommit{
		SHA:         strings.TrimSpace(parts[0]),
		Author:      strings.TrimSpace(parts[1]),
		Subject:     strings.TrimSpace(parts[2]),
		CommittedAt: committedAt,
		PatchID:     patchID(repoRoot, sha),
	}
	if m := revertTrailer.FindStringSubmatch(parts[4]); m != nil {
		c.RevertsSHA = strings.ToLower(m[1])
	}

	files, err := gitOutput(repoRoot, "show", "--name-only", "--format=", "--no-renames", sha)
	if err != nil {
		return c, fmt.Errorf("reading files of %s: %w", sha, err)
	}
	for _, line := range strings.Split(files, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			c.Files = append(c.Files, line)
		}
	}
	return c, nil
}

// parseCommitTime reads git's `%cI` and returns it **in UTC**.
//
// This normalization is load-bearing, not tidiness. `%cI` is strict ISO-8601
// with the committer's *local offset* (`2026-07-28T14:02:33+02:00`), while
// every event timestamp in the store is RFC3339Nano UTC and every window
// comparison in §D0 and §D5.1 is a lexicographic string BETWEEN. String
// comparison across mixed offsets is not chronological order: a commit made at
// 14:02+02:00 (12:02Z) sorts *after* a session that ended at 13:00Z, so it
// would fall outside a window it actually falls inside. The failure is silent
// mis-attribution — the report simply shows fewer attributed pairs — which is
// precisely what an adversarial reader of this ADR would hunt for.
func parseCommitTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("unparseable committer date %q: %w", raw, err)
	}
	return t.UTC(), nil
}

// patchID is `git patch-id --stable`, §D1.5's LOAD-BEARING dedup key: it
// survives rebases and cherry-picks, so reports can count distinct *changes*
// rather than distinct SHAs. Without it one rebase of a feature branch doubles
// every conform count.
//
// A commit with no diff (an empty or merge commit) has no patch id; the empty
// string is recorded honestly rather than substituting the SHA, which would
// silently defeat the dedup it exists for.
func patchID(repoRoot, sha string) string {
	show := exec.Command("git", "show", sha)
	show.Dir = repoRoot
	diff, err := show.Output()
	if err != nil {
		return ""
	}
	pid := exec.Command("git", "patch-id", "--stable")
	pid.Dir = repoRoot
	pid.Stdin = strings.NewReader(string(diff))
	out, err := pid.Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// FirstParentLog lists commit SHAs newest-first along the first-parent chain
// from ref, capped at max (§D1.2).
func FirstParentLog(repoRoot, ref string, max int) ([]string, error) {
	out, err := gitOutput(repoRoot, "log", "--first-parent", "--format=%H",
		fmt.Sprintf("-n%d", max), ref)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			shas = append(shas, line)
		}
	}
	return shas, nil
}

// DefaultBranch resolves the walk's second root: `origin/HEAD` if set, else
// `main`, else `master`. Returns "" when none resolves — a repo with only a
// detached HEAD is walked from HEAD alone.
func DefaultBranch(repoRoot string) string {
	if out, err := gitOutput(repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := gitOutput(repoRoot, "rev-parse", "--verify", "--quiet", candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// CurrentBranch is the advisory `branch` payload key (§D7: never joined on).
func CurrentBranch(repoRoot string) string {
	out, err := gitOutput(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ReachableCommits lists commits reachable from ref committed within
// [from, to], for §D4.4's completeness denominator. Computed live from git —
// never stored, because the denominator is a fact about the repository, not
// about us.
func ReachableCommits(repoRoot, ref string, from, to time.Time) ([]string, error) {
	out, err := gitOutput(repoRoot, "rev-list", ref,
		"--since="+from.UTC().Format(time.RFC3339),
		"--until="+to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			shas = append(shas, line)
		}
	}
	return shas, nil
}

// IsRepo reports whether repoRoot is inside a git work tree.
func IsRepo(repoRoot string) bool {
	out, err := gitOutput(repoRoot, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// HeadSHA resolves HEAD, or "" in a repo with no commits.
func HeadSHA(repoRoot string) string {
	out, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func gitOutput(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
