package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// RulesFiles are the rules-file sources, in the order the report lists them.
var RulesFiles = []string{"CLAUDE.md", "AGENTS.md", ".claude/CLAUDE.md", ".cursorrules"}

// ProbeRules reports the rules files present in a repo and how many blocks
// each holds.
func ProbeRules(root string) []Probe {
	var out []Probe
	for _, rel := range RulesFiles {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		blocks := splitBlocks(string(data))
		out = append(out, Probe{
			Source: rel, Available: true, Path: path,
			Count: len(blocks), Detail: plural(len(blocks), "block"),
		})
	}
	if mdc := cursorRuleFiles(root); len(mdc) > 0 {
		out = append(out, Probe{
			Source: ".cursor/rules", Available: true,
			Path:  filepath.Join(root, ".cursor", "rules"),
			Count: len(mdc), Detail: plural(len(mdc), "rule"),
		})
	}
	return out
}

// ImportRulesFile turns one rules file into **proposed conventions**, one per
// block (§D1).
//
// The promotion is not a heuristic about the prose: a rules file's *contract*
// is normative — it contains nothing but instructions to agents — so every
// block is a convention candidate by construction. That is the only reason
// this source may promote where claude-mem may not. `--as-notes` (asNotes)
// exists because a user may disagree about their own file, and falsifier 5
// watches whether that should become the default.
//
// Scope is `scope=[]`: the canonical repo-wide form per decisions-log item 5,
// and the honest one — a CLAUDE.md block states no file globs, and §D2.5
// forbids inventing them.
func ImportRulesFile(root, relPath string, asNotes bool) ([]Candidate, error) {
	data, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return nil, err
	}
	blocks := splitBlocks(string(data))
	out := make([]Candidate, 0, len(blocks))
	for _, b := range blocks {
		c := Candidate{
			// §D2.2: content-hashed, because these files have no stable row
			// IDs. Editing a block retires the old ref and imports the new text
			// as a new candidate — correct, because the text changed.
			SourceRef: relPath + "#" + blockHash(b.body),
			Title:     b.title,
			Content:   b.body,
			Tags:      []string{"rules-file"},
		}
		if !asNotes {
			c.AsDecision = true
			c.Kind = "convention"
		}
		out = append(out, c)
	}
	return out, nil
}

// ImportCursorRules reads `.cursor/rules/*.mdc` — the one source in the field
// that carries real scopes (§D1).
//
// `globs:` maps directly onto ADR-0001 scope. Invalid globs demote the entry
// to `scope=[]` with a report warning rather than failing the import, and
// `alwaysApply: true` maps to `scope=[]` (the canonical repo-wide form), not
// to `["**"]` — which L10 would then flag as under-thought.
func ImportCursorRules(root string, asNotes bool) ([]Candidate, []string, error) {
	var out []Candidate
	var warnings []string
	for _, path := range cursorRuleFiles(root) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		front, body := parseMDCFrontmatter(string(data))
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		title := strings.TrimSpace(front["description"])
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(path), ".mdc")
		}
		var scope []string
		if !strings.EqualFold(strings.TrimSpace(front["alwaysapply"]), "true") {
			for _, g := range splitPaths(front["globs"]) {
				if !doublestar.ValidatePattern(g) {
					warnings = append(warnings, rel+": invalid glob "+g+" — imported unscoped")
					scope = nil
					break
				}
				scope = append(scope, g)
			}
		}
		c := Candidate{
			SourceRef: rel + "#" + blockHash(body),
			Title:     title,
			Content:   body,
			Scope:     scope,
			Tags:      []string{"cursor-rules"},
		}
		if !asNotes {
			c.AsDecision = true
			c.Kind = "convention"
		}
		out = append(out, c)
	}
	return out, warnings, nil
}

func cursorRuleFiles(root string) []string {
	matches, err := filepath.Glob(filepath.Join(root, ".cursor", "rules", "*.mdc"))
	if err != nil {
		return nil
	}
	return matches
}

type block struct{ title, body string }

// splitBlocks splits a rules file into candidate blocks: markdown headings own
// their following prose; a plain-text file (.cursorrules) splits on blank
// lines, as cursor.go does today.
func splitBlocks(text string) []block {
	lines := strings.Split(text, "\n")
	hasHeading := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			hasHeading = true
			break
		}
	}
	var out []block
	if hasHeading {
		var cur block
		flush := func() {
			cur.body = strings.TrimSpace(cur.body)
			if cur.body != "" {
				out = append(out, cur)
			}
			cur = block{}
		}
		inFence := false
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "```") {
				inFence = !inFence
			}
			if !inFence && strings.HasPrefix(trimmed, "#") {
				flush()
				cur.title = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
				continue
			}
			cur.body += l + "\n"
		}
		flush()
		return out
	}
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		out = append(out, block{title: firstLine(para), body: para})
	}
	return out
}

// blockHash is §D2.2's `sha256[:16] of whitespace-normalized block`. The
// normalization is ADR-0002 P5.4's fallback rule, shared with the linter's
// duplicate check so the two agree on what "the same text" means.
func blockHash(s string) string {
	sum := sha256.Sum256([]byte(NormalizeText(s)))
	return hex.EncodeToString(sum[:])[:16]
}

// NormalizeText collapses whitespace runs, trims and lowercases (ADR-0002
// P5.4; reused by lint L5/L6 so hashing and duplicate detection cannot drift).
func NormalizeText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func parseMDCFrontmatter(text string) (map[string]string, string) {
	out := map[string]string{}
	if !strings.HasPrefix(text, "---") {
		return out, text
	}
	rest := strings.TrimPrefix(text, "---")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out, text
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if i := strings.Index(line, ":"); i > 0 {
			out[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
		}
	}
	body := rest[end+4:]
	return out, strings.TrimPrefix(body, "\n")
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return itoa(n) + " " + word + "s"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
