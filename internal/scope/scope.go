// Package scope implements the decision scope vocabulary: file globs, and
// nothing else, for the 90 days (Phase 0 ruling 4, ADR-0001 D4/D6).
//
// Matching is deterministic and case-sensitive against repo-relative paths.
// No inference, no model call, ever.
package scope

import (
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/memtrace-dev/memtrace/internal/types"
)

// ValidatePattern reports whether p is a syntactically valid glob. Whether it
// matches any existing file is deliberately not checked here — a decision may
// legitimately scope files that do not exist yet (D4; the linter warns).
func ValidatePattern(p string) error {
	if strings.TrimSpace(p) == "" {
		return &types.ValidationError{Field: "scope", Message: "glob pattern must not be empty"}
	}
	if !doublestar.ValidatePattern(p) {
		return &types.ValidationError{Field: "scope", Message: "invalid glob pattern: " + p}
	}
	return nil
}

// Validate checks every entry of a scope list.
func Validate(scope []string) error {
	for _, p := range scope {
		if err := ValidatePattern(p); err != nil {
			return err
		}
	}
	return nil
}

// Normalize cleans a repo-relative path for matching: forward slashes, no
// leading "./", no leading "/".
func Normalize(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return p
	}
	return path.Clean(p)
}

// Matches reports whether the repo-relative path matches any glob in scope.
// An empty scope matches nothing: empty scope means "never scope-matched",
// for both kinds (D4 — packer candidacy and observer matching are different
// questions, and an empty-scope convention is still never scope-matched).
func Matches(scope []string, filePath string) bool {
	fp := Normalize(filePath)
	for _, g := range scope {
		ok, err := doublestar.Match(Normalize(g), fp)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// MatchedGlobs returns the globs in scope that match any of the given paths,
// in scope order, deduplicated. This is the `matched_globs` payload key of
// diff.scope_match and decision.violated (D7).
func MatchedGlobs(scope []string, filePaths []string) []string {
	var out []string
	for _, g := range scope {
		ng := Normalize(g)
		for _, fp := range filePaths {
			ok, err := doublestar.Match(ng, Normalize(fp))
			if err == nil && ok {
				out = append(out, g)
				break
			}
		}
	}
	return out
}

// MatchedFiles returns the paths matched by any glob in scope, in input order.
func MatchedFiles(scope []string, filePaths []string) []string {
	var out []string
	for _, fp := range filePaths {
		if Matches(scope, fp) {
			out = append(out, fp)
		}
	}
	return out
}
