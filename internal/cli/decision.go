package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/spf13/cobra"
)

// newDecisionCmd is the human-confirmation surface for the decision lifecycle
// (ADR-0001 D2, open question 3: CLI and TUI only — MCP may propose but never
// accept, because an agent asserting "the user approved" is exactly the
// assertion the quarantine exists to distrust).
//
// Without it the quarantine is incoherent: four paths create proposals (MCP,
// `import`, the git importer, the v1→v2 migration) and none can leave one.
func newDecisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decision",
		Short: "Review and act on the decision lifecycle",
		Long: "Decisions saved by an agent, an importer or the v1 migration land " +
			"`proposed`: they neither bind nor pack until a human accepts them.",
	}
	cmd.AddCommand(newDecisionPendingCmd(), newDecisionAcceptCmd(), newDecisionRejectCmd(),
		newDecisionPromoteCmd())
	return cmd
}

// newDecisionPendingCmd shows the confirmation queue.
func newDecisionPendingCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List decisions awaiting human confirmation",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			ds, err := k.Decisions().ListDecisions(kernel.DecisionFilter{
				Statuses: []types.DecisionStatus{types.StatusProposed},
				Limit:    limit,
			})
			if err != nil {
				return err
			}
			if len(ds) == 0 {
				fmt.Println("No decisions are awaiting confirmation.")
				return nil
			}
			dim := color.New(color.Faint)
			for i, d := range ds {
				fmt.Printf("[%d] %s  ", i+1, typeColorFor(types.MemoryType(d.Kind)).Sprint(string(d.Kind)))
				dim.Printf("%s", shortID(d.ID))
				color.New(color.FgMagenta).Printf("  [proposed]")
				fmt.Println()
				fmt.Printf("    %s\n", d.Title)
				meta := []string{"source: " + string(d.Source)}
				if len(d.Scope) > 0 {
					meta = append(meta, "scope: "+strings.Join(d.Scope, ", "))
				}
				dim.Printf("    %s\n", strings.Join(meta, " | "))
				fmt.Println()
			}
			dim.Printf("Accept with 'memtrace decision accept <id>', decline with 'memtrace decision reject <id>'.\n")
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "Max results")
	return cmd
}

func newDecisionAcceptCmd() *cobra.Command {
	var force bool
	var evidence []string
	cmd := &cobra.Command{
		Use:   "accept <id|prefix>",
		Short: "Accept a proposed decision (proposed → active)",
		Long: "Acceptance is the human confirmation the quarantine waits for. It requires " +
			"at least one evidence row unless --force is passed, and a forced acceptance " +
			"is recorded in the audit trail (ADR-0001 D4).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id, err := resolveDecisionID(k, args[0])
			if err != nil {
				return err
			}

			// Parse every --evidence value, and check the decision can actually be
			// accepted, *before* writing anything. Attaching first meant a failed
			// acceptance left evidence rows the user did not get the decision for,
			// and `accept <terminal-id> --evidence …` attached evidence to a
			// terminal decision and only then reported the illegal transition
			// (F23).
			inputs := make([]kernel.EvidenceInput, 0, len(evidence))
			for _, spec := range evidence {
				in, parseErr := parseEvidence(spec)
				if parseErr != nil {
					return parseErr
				}
				inputs = append(inputs, in)
			}
			current, err := k.Decisions().GetDecision(id)
			if err != nil {
				return err
			}
			if current.Status != types.StatusProposed {
				return notProposedError(id, current.Status)
			}

			for i, in := range inputs {
				_, addErr := k.Decisions().AddEvidence(id, in)
				// A duplicate is the benign case — the row the user asked for is
				// already attached — so it is reported and the acceptance
				// continues, rather than surfacing a constraint abort.
				if errors.Is(addErr, types.ErrDuplicateEvidence) {
					color.New(color.Faint).Printf("  %s (already attached)\n", evidence[i])
					continue
				}
				if addErr != nil {
					return fmt.Errorf("attaching evidence %q: %w", evidence[i], addErr)
				}
			}

			d, err := k.Decisions().Accept(id, kernel.AcceptOptions{
				Force: force,
				Actor: types.ActorHuman,
			})
			if errors.Is(err, types.ErrNoEvidence) {
				return fmt.Errorf("%w\n  attach evidence with --evidence commit:<sha> (or pr/test/file/url/import), "+
					"or accept unevidenced with --force", err)
			}
			var illegal *types.IllegalTransitionError
			if errors.As(err, &illegal) {
				return notProposedError(id, illegal.From)
			}
			if err != nil {
				return err
			}
			fmt.Printf("Accepted %s: %s\n", d.ID, d.Title)
			if d.TopicKey != "" {
				color.New(color.Faint).Printf("  topic_key %s transferred\n", d.TopicKey)
			}
			for _, pred := range d.Supersedes {
				color.New(color.Faint).Printf("  superseded %s\n", pred)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"Accept with no evidence; recorded as \"forced\" in the audit trail")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil,
		"Evidence to attach before accepting, as kind:ref (commit, pr, test, file, url, import)")
	return cmd
}

func newDecisionRejectCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "reject <id|prefix>",
		Short: "Decline a proposed decision (proposed → rejected)",
		Long: "Rejection is a terminal disposal state, not a delete: the audit record " +
			"survives, because \"we considered X and said no\" is exactly what a later " +
			"session needs (ADR-0001 D2).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id, err := resolveDecisionID(k, args[0])
			if err != nil {
				return err
			}
			if err := k.Decisions().Reject(id, reason); err != nil {
				return err
			}
			fmt.Printf("Rejected %s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the proposal was declined (recorded in the event)")
	return cmd
}

// newDecisionPromoteCmd implements ADR-0001 Amendment 2, A2.3.
func newDecisionPromoteCmd() *cobra.Command {
	var title, kind, scope string
	cmd := &cobra.Command{
		Use:   "promote <note-id|prefix>",
		Short: "Turn a note into a proposed decision",
		Long: "\"I wrote this as a note and now realise it is a decision\" — promotion " +
			"creates a proposed decision from the note, through the ordinary lifecycle, " +
			"so it is born with provenance and a quarantine. The note stays live while " +
			"the promotion is pending and is archived when the decision is accepted; " +
			"rejecting the promotion leaves the note untouched. CLI and TUI only.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id := resolveID(k, args[0])
			if id == "" {
				return fmt.Errorf("note %s not found", args[0])
			}

			over := kernel.PromoteOverrides{Title: title, Kind: types.DecisionKind(kind)}
			if cmd.Flags().Changed("scope") {
				over.Scope = []string{}
				for _, g := range strings.Split(scope, ",") {
					if g = strings.TrimSpace(g); g != "" {
						over.Scope = append(over.Scope, g)
					}
				}
			}

			d, err := k.PromoteNote(id, over)
			if err != nil {
				return err
			}
			fmt.Printf("Proposed %s (%s): %s\n", d.ID, d.Kind, d.Title)
			color.New(color.Faint).Printf(
				"  the note stays live until you run 'memtrace decision accept %s'\n",
				shortID(d.ID))
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "Override the title (default: the note's summary)")
	cmd.Flags().StringVar(&kind, "kind", "", "decision or convention (default: decision)")
	cmd.Flags().StringVar(&scope, "scope", "",
		"Comma-separated file globs (default: the note's file paths, verbatim)")
	return cmd
}

// notProposedError explains why a non-proposal cannot be accepted. Acceptance
// is the proposed→active edge only: re-accepting is not idempotent-and-
// harmless, it would re-promote evidence attached since (§D4, F19).
func notProposedError(id string, status types.DecisionStatus) error {
	switch status {
	case types.StatusActive:
		return fmt.Errorf("decision %s is already active; it can only be accepted once, "+
			"and re-accepting would promote evidence attached since to accepting evidence", id)
	case types.StatusViolated:
		return fmt.Errorf("decision %s is violated, and still binding; it returns to active "+
			"when its violations are resolved, not by being accepted again", id)
	default:
		return fmt.Errorf("decision %s is %s — only a proposal can be accepted", id, status)
	}
}

// parseEvidence parses a "kind:ref" flag value. The kind is required and
// validated by the kernel; being explicit keeps `--evidence 0a1b2c` from
// silently becoming a commit reference that does not exist.
func parseEvidence(spec string) (kernel.EvidenceInput, error) {
	kind, ref, ok := strings.Cut(spec, ":")
	if !ok || strings.TrimSpace(ref) == "" {
		return kernel.EvidenceInput{}, fmt.Errorf(
			"--evidence %q: expected kind:ref, e.g. commit:9f2c1ab", spec)
	}
	return kernel.EvidenceInput{
		Kind:    types.EvidenceKind(strings.TrimSpace(kind)),
		Ref:     strings.TrimSpace(ref),
		AddedBy: types.ActorHuman,
	}, nil
}

// resolveDecisionID resolves a full ULID or a short prefix against the
// `decisions` table, across every status. resolveID's projection-based lookup
// is not usable here: it is for memories, and a lifecycle command must be able
// to name a row in any state, including the terminal ones.
func resolveDecisionID(k *kernel.MemoryKernel, prefix string) (string, error) {
	upper := strings.ToUpper(strings.TrimSpace(prefix))
	if len(upper) >= 26 {
		if _, err := k.Decisions().GetDecision(upper); err != nil {
			return "", fmt.Errorf("decision %s not found", prefix)
		}
		return upper, nil
	}
	all, err := k.Decisions().ListDecisions(kernel.DecisionFilter{})
	if err != nil {
		return "", err
	}
	var matches []string
	for _, d := range all {
		if strings.HasPrefix(d.ID, upper) {
			matches = append(matches, d.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("decision %s not found", prefix)
	default:
		return "", fmt.Errorf("decision %s is ambiguous: %d decisions share that prefix",
			prefix, len(matches))
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// pendingProposalCount reports how many decisions are awaiting confirmation.
func pendingProposalCount(k *kernel.MemoryKernel) int {
	n, err := k.Decisions().CountDecisions(kernel.DecisionFilter{
		Statuses: []types.DecisionStatus{types.StatusProposed},
	})
	if err != nil {
		return 0
	}
	return n
}
