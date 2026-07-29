package ingestion

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/varve-sh/varve/internal/types"
)

// decisionKeywords are words that signal a commit contains a notable decision.
var decisionKeywords = []string{
	"decided", "chose", "switched to", "migrated", "replaced",
	"deprecated", "refactored", "convention", "pattern",
	"architecture", "breaking change", "reverted",
}

// ImportGitHistory imports decision-like memories from recent git commit messages.
func ImportGitHistory(projectRoot string, maxCommits int) ([]types.MemorySaveInput, error) {
	if maxCommits <= 0 {
		maxCommits = 100
	}

	cmd := exec.Command("git", "log",
		"--format=%H%n%s%n%b%n---VARVE_END---",
		"-n", fmt.Sprintf("%d", maxCommits),
	)
	cmd.Dir = projectRoot

	out, err := cmd.Output()
	if err != nil {
		return nil, nil // not a git repo or git not available
	}

	var results []types.MemorySaveInput
	blocks := strings.Split(string(out), "---VARVE_END---")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.SplitN(block, "\n", 3)
		if len(lines) < 2 {
			continue
		}
		hash := strings.TrimSpace(lines[0])
		subject := strings.TrimSpace(lines[1])
		body := ""
		if len(lines) == 3 {
			body = strings.TrimSpace(lines[2])
		}

		if !isDecisionCommit(subject, body) {
			continue
		}

		content := subject
		if body != "" {
			content = subject + "\n\n" + body
		}

		results = append(results, types.MemorySaveInput{
			Content:    content,
			Type:       types.MemoryTypeDecision,
			Summary:    truncateStr(subject, 120),
			Source:     types.MemorySourceGit,
			SourceRef:  "git:" + hash,
			Confidence: 0.7,
			Tags:       []string{"imported", "git"},
			FilePaths:  []string{},
		})
	}
	return results, nil
}

func isDecisionCommit(subject, body string) bool {
	combined := strings.ToLower(subject + " " + body)
	for _, kw := range decisionKeywords {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	// Long body likely contains useful context
	return len(body) > 100
}
