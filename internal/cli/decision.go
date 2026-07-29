package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
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
		newDecisionRevertCmd(), newDecisionPromoteCmd(), newDecisionPurgeCmd())
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
			// An agent's forget of a decision records a request and transitions
			// nothing (ADR-0001 Amendment 3). The human confirmation is only
			// "one keystroke away" if the request is visible where proposals are
			// triaged, so it is listed here — including for binding decisions,
			// which never appear in the proposals list.
			disposals, err := k.Decisions().PendingDisposals("")
			if err != nil {
				return err
			}
			requested := make(map[string]kernel.DisposalRequest, len(disposals))
			for _, r := range disposals {
				requested[r.Decision.ID] = r
			}
			if len(ds) == 0 && len(disposals) == 0 {
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
				if r, ok := requested[d.ID]; ok {
					printDisposalRequest(r)
				}
				fmt.Println()
			}

			// Binding decisions an agent asked to have disposed of. They are not
			// proposals, so nothing else lists them for review.
			var binding []kernel.DisposalRequest
			for _, r := range disposals {
				if r.Decision.Status != types.StatusProposed {
					binding = append(binding, r)
				}
			}
			if len(binding) > 0 {
				color.New(color.Bold).Printf("Disposal requested by an agent:\n\n")
				for i, r := range binding {
					fmt.Printf("[%d] %s  ", i+1,
						typeColorFor(types.MemoryType(r.Decision.Kind)).Sprint(string(r.Decision.Kind)))
					dim.Printf("%s", shortID(r.Decision.ID))
					color.New(color.FgRed).Printf("  [%s]", r.Decision.Status)
					fmt.Println()
					fmt.Printf("    %s\n", r.Decision.Title)
					printDisposalRequest(r)
					fmt.Println()
				}
				dim.Printf("Confirm with 'varve decision revert <id>', or leave it binding by doing nothing.\n")
			}
			if len(ds) > 0 {
				dim.Printf("Accept with 'varve decision accept <id>', decline with 'varve decision reject <id>'.\n")
			}
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
				return notProposedError(id, current.Status, "accepted")
			}

			for i, in := range inputs {
				_, addErr := k.AddDecisionEvidence(id, in)
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

			d, err := k.AcceptDecision(id, kernel.AcceptOptions{
				Force: force,
				Actor: types.ActorHuman,
			})
			if errors.Is(err, types.ErrNoEvidence) {
				return fmt.Errorf("%w\n  attach evidence with --evidence commit:<sha> (or pr/test/file/url/import), "+
					"or accept unevidenced with --force", err)
			}
			var illegal *types.IllegalTransitionError
			if errors.As(err, &illegal) {
				return notProposedError(id, illegal.From, "accepted")
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

// printDisposalRequest renders an agent's pending request to dispose of a
// decision. Nothing has happened to the row yet — the wording must not suggest
// otherwise (ADR-0001 Amendment 3).
func printDisposalRequest(r kernel.DisposalRequest) {
	msg := "    disposal requested by an agent"
	if r.Count > 1 {
		msg += fmt.Sprintf(" (%d times)", r.Count)
	}
	if r.Reason != "" {
		msg += ": " + r.Reason
	}
	color.New(color.FgYellow).Printf("%s\n", msg)
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
			err = k.RejectDecision(id, reason)
			var illegal *types.IllegalTransitionError
			if errors.As(err, &illegal) {
				// `accept` got a translation layer and `reject` did not; a raw
				// transition error is the class the ADRs legislate against (F35).
				return notProposedError(id, illegal.From, "declined")
			}
			if err != nil {
				return err
			}
			fmt.Printf("Rejected %s\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the proposal was declined (recorded in the event)")
	return cmd
}

func newDecisionRevertCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revert <id|prefix>",
		Short: "Repeal a binding decision (active|violated → reverted)",
		Long: "A repeal is terminal: re-adopting the rule later means a new decision " +
			"citing this one, never a resurrection (ADR-0001 D3/D5). This is the human " +
			"confirmation an agent's disposal request waits for.",
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
			// A proposal cannot be reverted — §D3 says proposed→reverted is
			// illegal and to reject instead. Doing the rejection silently would
			// perform an irreversible terminal action under a name the user did
			// not type; F19 established the pattern for exactly this case, which
			// is to refuse and say which command to run (F35).
			current, err := k.Decisions().GetDecision(id)
			if err != nil {
				return err
			}
			if current.Status == types.StatusProposed {
				return fmt.Errorf("decision %s is still a proposal, so there is nothing to "+
					"repeal — decline it with `varve decision reject %s`, which is also "+
					"terminal but is the transition §D3 defines for a proposal", id, shortID(id))
			}

			outcome, err := k.Forget(id, types.ActorHuman)
			if err != nil {
				return err
			}
			switch outcome {
			case kernel.DisposalReverted:
				fmt.Printf("Reverted %s — kept as a reverted audit record\n", id)
			default:
				fmt.Printf("Decision %s is already %s; nothing to revert\n", id, current.Status)
			}
			return nil
		},
	}
	return cmd
}

// newDecisionPurgeCmd implements ADR-0001 Amendment 4. It is the only
// destructive verb in the product, and everything about it is deliberate: the
// name states the contract, the channel is human-only, the id must be typed
// back, and what it cannot reach is printed rather than implied.
func newDecisionPurgeCmd() *cobra.Command {
	var reason string
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge <id>",
		Short: "Irreversibly remove a decision's content (e.g. a leaked secret)",
		Long: "Purge is not forget. `rm`, `decision reject` and `decision revert` all keep " +
			"the decision as an audit record; purge destroys its content and cannot be " +
			"undone.\n\n" +
			"A decision with history is redacted in place — its events are append-only and " +
			"its id is referenced by the attribution trail, so the row survives as a " +
			"`[purged]` tombstone. A decision with no history at all (one carried over by " +
			"`migrate --from-v1` and untouched since) is deleted outright, leaving a " +
			"tombstone event.\n\n" +
			"There is no MCP equivalent, by design.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, projectRoot, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()

			id, err := resolveDecisionID(k, args[0])
			if err != nil {
				return err
			}
			d, err := k.Decisions().GetDecision(id)
			if err != nil {
				return err
			}

			bold := color.New(color.Bold)
			dim := color.New(color.Faint)
			bold.Printf("About to purge %s\n", id)
			fmt.Printf("  %s · %s\n", d.Kind, d.Title)
			migrationBorn, err := k.Decisions().MigrationBorn(id)
			if err != nil {
				return err
			}
			if migrationBorn {
				color.New(color.FgRed).Printf(
					"  This decision has no recorded history, so the row will be DELETED. " +
						"A tombstone event records that it existed.\n")
			} else {
				color.New(color.FgRed).Printf(
					"  Its content will be cleared and the decision moved to a terminal state. " +
						"The row and its events survive as a `[purged]` tombstone.\n")
			}
			fmt.Println()
			dim.Printf("This cannot be undone. Copies purge cannot reach:\n")
			for _, r := range kernel.PurgeResidue(projectRoot) {
				dim.Printf("  - %s\n", r)
			}
			fmt.Println()

			// The id must be typed back. A y/n prompt is the wrong ceremony for
			// the one irreversible action in the product: it is answered by
			// reflex, and the cost of a mistaken keystroke here is a decision
			// that cannot be recovered from the store.
			if !yes {
				fmt.Printf("Type the full id to confirm: ")
				var typed string
				if _, scanErr := fmt.Fscanln(cmd.InOrStdin(), &typed); scanErr != nil {
					return fmt.Errorf("purge cancelled")
				}
				if strings.TrimSpace(typed) != id {
					return fmt.Errorf("purge cancelled: %q does not match %s", typed, id)
				}
			}

			res, err := k.Purge(id, reason, types.ActorHuman)
			if err != nil {
				return err
			}
			switch res.Arm {
			case kernel.PurgeDeleted:
				fmt.Printf("Purged %s — the row was deleted (%d evidence rows); "+
					"a decision.purged event records it\n", id, res.EvidenceRows)
			default:
				if res.Transitioned != "" {
					fmt.Printf("Purged %s — content cleared, decision %s\n", id, res.Transitioned)
				} else {
					fmt.Printf("Purged %s — content cleared\n", id)
				}
			}
			dim.Printf("Handle the copies listed above yourself; the store cannot.\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "secret",
		"Why it was purged: secret or cleanup (recorded in the event)")
	cmd.Flags().BoolVar(&yes, "yes", false,
		"Skip the typed confirmation (for scripts; the action is still irreversible)")
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
				"  the note stays live until you run 'varve decision accept %s'\n",
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
func notProposedError(id string, status types.DecisionStatus, verb string) error {
	if verb == "" {
		verb = "accepted"
	}
	switch {
	case status == types.StatusActive && verb == "accepted":
		return fmt.Errorf("decision %s is already active; it can only be accepted once, "+
			"and re-accepting would promote evidence attached since to accepting evidence", id)
	case status == types.StatusViolated && verb == "accepted":
		return fmt.Errorf("decision %s is violated, and still binding; it returns to active "+
			"when its violations are resolved, not by being accepted again", id)
	case status.IsTerminal():
		return fmt.Errorf("decision %s is %s, which is terminal — it cannot be %s. "+
			"Re-adopting a rule means a new decision citing this one", id, status, verb)
	default:
		return fmt.Errorf("decision %s is %s, and only a proposal can be %s — "+
			"repeal a binding decision with `varve decision revert %s`",
			id, status, verb, shortID(id))
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
