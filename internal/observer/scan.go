package observer

import (
	"fmt"
	"time"

	"github.com/memtrace-dev/memtrace/internal/kernel"
)

// consecutiveObservedStop is §D1.2's walk terminator: 20, not 1.
//
// One would be wrong in a way that only shows up after a merge: interleaved
// histories mean an unobserved commit can sit behind an observed one, and a
// stop-at-first walk strands it forever. Twenty is cheap (the cursor check is
// an indexed lookup) and covers ordinary merge interleaving.
const consecutiveObservedStop = 20

// defaultWalkCap bounds a single scan so a fresh install on an old repository
// cannot spend minutes walking history it will discard at the epoch anyway.
const defaultWalkCap = 500

// ScanOptions controls a catch-up walk (§D1.2).
type ScanOptions struct {
	RepoRoot string
	// Backfill walks past the observation epoch, and marks everything it
	// produces (§D1.3). Off by default: verdicts about commits that predate the
	// decision store are archaeology, not attribution, and mixing them into the
	// headline numbers is the first thing an auditor catches.
	Backfill bool
	// Limit caps commits walked per root. Zero means defaultWalkCap.
	Limit int
}

// ScanResult summarises a walk, for `varve scan` and for the report's
// observer-completeness line.
type ScanResult struct {
	Walked          int
	Observed        int
	AlreadyObserved int
	SkippedPreEpoch int
	Matched         int
	Violated        int
	Reverted        []string
	Reinstated      []string
	Errors          []string
}

// Scan walks the commits not yet observed and observes them (§D1.2).
//
// Roots: HEAD, and the default branch head. Newest first along first-parent,
// stopping per root after 20 consecutive already-observed commits or the walk
// cap. There is no cursor table — a commit is "already observed" iff its
// `diff.observed` row exists, which `idx_events_observed_once` makes airtight.
//
// This is the half of §D1 that makes the record complete: the hook covers
// commits made on this machine while varve was installed, and the scan covers
// everything else — commits made before install, pulled from teammates, made
// with the hook bypassed, or lost to a busy database (the hook never retries;
// it exits and lets the scan pick the commit up).
func Scan(k *kernel.MemoryKernel, opts ScanOptions) (*ScanResult, error) {
	res := &ScanResult{}
	if !IsRepo(opts.RepoRoot) {
		return res, nil // not a git repo: nothing to observe, not an error
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultWalkCap
	}

	var epoch *time.Time
	if !opts.Backfill {
		e, err := k.ObserverEpoch()
		if err != nil {
			return nil, err
		}
		epoch = e
	}

	roots := []string{"HEAD"}
	if def := DefaultBranch(opts.RepoRoot); def != "" {
		roots = append(roots, def)
	}

	seen := make(map[string]bool)
	for _, root := range roots {
		shas, err := FirstParentLog(opts.RepoRoot, root, limit)
		if err != nil {
			continue // an unborn branch or a bad ref is not a failure of the scan
		}
		consecutive := 0
		for _, sha := range shas {
			if seen[sha] {
				continue
			}
			seen[sha] = true
			res.Walked++

			observed, err := k.IsObserved(sha)
			if err != nil {
				return nil, err
			}
			if observed {
				res.AlreadyObserved++
				consecutive++
				if consecutive >= consecutiveObservedStop {
					break
				}
				continue
			}
			consecutive = 0

			c, err := Show(opts.RepoRoot, sha)
			if err != nil {
				res.Errors = append(res.Errors, err.Error())
				continue
			}
			// §D1.3: the scan does not walk past the epoch. Pre-epoch commits
			// are observed only by an explicit backfill, and everything a
			// backfill produces is marked and excluded from the report.
			if epoch != nil && c.CommittedAt.Before(*epoch) {
				res.SkippedPreEpoch++
				continue
			}
			c.Backfill = opts.Backfill
			c.Branch = root

			out, err := k.ObserveCommit(c)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", sha[:min(7, len(sha))], err))
				continue
			}
			if out.AlreadyObserved {
				res.AlreadyObserved++
				continue
			}
			res.Observed++
			res.Matched += out.Matched
			res.Violated += out.Violated
			res.Reverted = append(res.Reverted, out.DecisionsReverted...)
			res.Reinstated = append(res.Reinstated, out.DecisionsReinstated...)
		}
	}
	return res, nil
}

// ObserveOne observes a single commit — the hook's path (`varve observe
// --commit HEAD`).
//
// The epoch is not consulted: an explicit observation of a named commit is the
// user (or the hook) saying "this one", and the epoch exists to stop the *walk*
// from wandering into history, not to refuse a commit made right now.
func ObserveOne(k *kernel.MemoryKernel, repoRoot, ref string) (*kernel.ObservationResult, error) {
	if !IsRepo(repoRoot) {
		return &kernel.ObservationResult{}, nil
	}
	c, err := Show(repoRoot, ref)
	if err != nil {
		return nil, err
	}
	c.Branch = CurrentBranch(repoRoot)
	return k.ObserveCommit(c)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
