package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// hookLine is ADR-0004 §D1.1's post-commit hook, verbatim in shape.
//
// Every clause is load-bearing. `command -v` makes a machine without the
// binary on PATH run a no-op — a teammate who clones the repo and does not use
// varve must not see errors. `&` puts the work in the background, so the
// commit's perceived latency is a shell fork. `--quiet` suppresses output and
// forces exit 0, so the hook can neither fail nor speak. The comment names the
// recovery path, because a hook a user cannot safely delete is a hook they
// will delete angrily.
//
// The output redirection is not belt-and-braces: found by running this against
// a machine with an *older* varve on PATH, which does not know the
// `observe` subcommand and printed a cobra usage error into the middle of a
// commit. `--quiet` can only silence a binary that understands it, so §D7's
// "never prints" has to be structural.
const hookLine = `command -v varve >/dev/null 2>&1 && varve observe --commit HEAD --quiet >/dev/null 2>&1 &`

const hookHeader = "# installed by varve — safe to delete; `varve scan` recovers missed commits"

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage the git hooks that feed attribution",
	}
	cmd.AddCommand(newHooksInstallCmd())
	return cmd
}

func newHooksInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the post-commit hook that observes commits",
		Long: "Writes .git/hooks/post-commit (ADR-0004 §D1.1). The hook runs varve in " +
			"the background, prints nothing, and cannot fail a commit. An existing " +
			"hook is appended to, never overwritten.",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, projectRoot, err := openKernel()
			if err != nil {
				return err
			}

			// §OQ5: a repository with core.hooksPath set does not use
			// .git/hooks at all. Detect it and say so rather than writing a
			// file git will ignore.
			if custom := gitConfig(projectRoot, "core.hooksPath"); custom != "" {
				fmt.Printf("This repository sets core.hooksPath = %s, so .git/hooks is not used.\n", custom)
				fmt.Printf("Add this line to %s/post-commit yourself:\n\n  %s\n\n", custom, hookLine)
				fmt.Println("Until then, `varve scan` still observes every commit — one session later.")
				return nil
			}

			hookPath, err := hooksDir(projectRoot)
			if err != nil {
				return err
			}
			hookPath = filepath.Join(hookPath, "post-commit")

			existing, readErr := os.ReadFile(hookPath)
			switch {
			case readErr != nil: // no hook yet
				body := "#!/bin/sh\n" + hookHeader + "\n" + hookLine + "\n"
				if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
					return fmt.Errorf("writing hook: %w", err)
				}
				fmt.Printf("Installed %s\n", hookPath)
			case strings.Contains(string(existing), "varve observe"):
				// Idempotent: chaining is normal, double-appending is not.
				fmt.Printf("%s already runs varve — nothing to do\n", hookPath)
			case !looksLikeShellScript(string(existing)):
				color.New(color.FgYellow).Printf(
					"%s exists and is not a shell script varve can safely append to.\n", hookPath)
				fmt.Printf("Add this line to it yourself:\n\n  %s\n", hookLine)
				return nil
			default:
				appended := strings.TrimRight(string(existing), "\n") + "\n\n" + hookHeader + "\n" + hookLine + "\n"
				if err := os.WriteFile(hookPath, []byte(appended), 0o755); err != nil {
					return fmt.Errorf("appending to hook: %w", err)
				}
				fmt.Printf("Appended varve to the existing %s\n", hookPath)
			}

			color.New(color.Faint).Println(
				"The hook never blocks, never prints, and cannot fail a commit. " +
					"`varve scan` recovers anything it misses.")
			return nil
		},
	}
}

// hooksDir resolves .git/hooks, following the gitdir pointer a worktree uses.
func hooksDir(projectRoot string) (string, error) {
	out, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return "", fmt.Errorf("this does not look like a git repository: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectRoot, dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func gitConfig(projectRoot, key string) string {
	out, err := exec.Command("git", "-C", projectRoot, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func looksLikeShellScript(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return true
	}
	first := strings.SplitN(trimmed, "\n", 2)[0]
	return strings.HasPrefix(first, "#!") &&
		(strings.Contains(first, "sh") || strings.Contains(first, "bash") || strings.Contains(first, "zsh"))
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// installPostCommitHook is `init`'s best-effort hook installation (§D1.1).
// It is silent about failure: a repository that cannot take the hook still
// gets complete observation from `varve scan`, one session later.
func installPostCommitHook(projectRoot string) {
	if gitConfig(projectRoot, "core.hooksPath") != "" {
		return
	}
	dir, err := hooksDir(projectRoot)
	if err != nil {
		return
	}
	path := filepath.Join(dir, "post-commit")
	existing, readErr := os.ReadFile(path)
	if readErr == nil {
		if strings.Contains(string(existing), "varve observe") || !looksLikeShellScript(string(existing)) {
			return
		}
		body := strings.TrimRight(string(existing), "\n") + "\n\n" + hookHeader + "\n" + hookLine + "\n"
		if os.WriteFile(path, []byte(body), 0o755) == nil {
			fmt.Println("Added varve to the existing post-commit hook (it cannot fail or slow a commit).")
		}
		return
	}
	body := "#!/bin/sh\n" + hookHeader + "\n" + hookLine + "\n"
	if os.WriteFile(path, []byte(body), 0o755) == nil {
		fmt.Println("Installed a post-commit hook so commits are attributed (it cannot fail or slow a commit).")
	}
}
