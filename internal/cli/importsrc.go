package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/importer"
	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/lint"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
	"github.com/spf13/cobra"
)

// importFlags are §D2.1's flags, shared by every source.
type importFlags struct {
	dryRun  bool
	yes     bool
	asNotes bool
	format  string
	db      string
}

func (f *importFlags) register(cmd *cobra.Command, withDB bool) {
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Preview, save nothing")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "Skip confirmation")
	cmd.Flags().BoolVar(&f.asNotes, "as-notes", false,
		"Demote all decision candidates to notes")
	cmd.Flags().StringVar(&f.format, "format", "", "Report output: md, json (default: terminal text)")
	if withDB {
		cmd.Flags().StringVar(&f.db, "db", "", "Path to the source database")
	}
}

// sourceRun is one source's contribution to an import run.
type sourceRun struct {
	name       string
	candidates []importer.Candidate
	warnings   []string
}

func newImportSourceCmds() []*cobra.Command {
	var cmds []*cobra.Command

	claudeMem := &cobra.Command{
		Use:   "claude-mem",
		Short: "Import a claude-mem store (notes only)",
		Long: `Import observations from claude-mem's SQLite store.

claude-mem holds narrative session observations with no scope, no evidence and
no normative flag, so every row imports as a NOTE. Session archaeology is not a
rule, and nothing here is promoted by guesswork.`,
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	var cmFlags importFlags
	cmFlags.register(claudeMem, true)
	claudeMem.RunE = func(cmd *cobra.Command, args []string) error {
		path := cmFlags.db
		if path == "" {
			path = importer.DefaultClaudeMemDB()
		}
		cands, err := importer.ImportClaudeMem(path)
		if err != nil {
			return err
		}
		return runImport(cmd, []sourceRun{{name: "claude-mem", candidates: cands}}, cmFlags)
	}
	cmds = append(cmds, claudeMem)

	engram := &cobra.Command{
		Use:   "engram",
		Short: "Import an engram store (notes, plus proposed decisions where engram typed them)",
	}
	var enFlags importFlags
	enFlags.register(engram, true)
	engram.RunE = func(cmd *cobra.Command, args []string) error {
		path := enFlags.db
		if path == "" {
			path = importer.DefaultEngramDB()
		}
		cands, err := importer.ImportEngram(path, enFlags.asNotes)
		if err != nil {
			return err
		}
		return runImport(cmd, []sourceRun{{name: "engram", candidates: cands}}, enFlags)
	}
	cmds = append(cmds, engram)

	rules := &cobra.Command{
		Use:   "rules",
		Short: "Import CLAUDE.md, AGENTS.md, .cursorrules and .cursor/rules as proposed conventions",
		Long: `Import the repo's rules files.

A rules file's contract is normative — it contains nothing but instructions to
agents — so every block imports as a PROPOSED convention. Nothing binds until
you accept it, and --as-notes demotes the lot if you disagree.`,
	}
	var rFlags importFlags
	rFlags.register(rules, false)
	rules.RunE = func(cmd *cobra.Command, args []string) error {
		_, root, err := openKernel()
		if err != nil {
			return err
		}
		runs, err := rulesSources(root, rFlags.asNotes)
		if err != nil {
			return err
		}
		return runImport(cmd, runs, rFlags)
	}
	cmds = append(cmds, rules)

	undo := &cobra.Command{
		Use:   "undo [batch-id]",
		Short: "Undo an import batch (default: the most recent)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, _, err := openKernel()
			if err != nil {
				return err
			}
			defer k.Close()
			batch := ""
			if len(args) == 1 {
				batch = args[0]
			}
			res, err := k.UndoImport(batch)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Undid batch %s: %d notes deleted, %d proposals rejected\n",
				res.BatchID, res.NotesDeleted, res.DecisionsRejected)
			if len(res.LeftUntouched) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"Left untouched (you had already acted on these):\n")
				for _, id := range res.LeftUntouched {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", id)
				}
			}
			return nil
		},
	}
	cmds = append(cmds, undo)

	return cmds
}

func rulesSources(root string, asNotes bool) ([]sourceRun, error) {
	var runs []sourceRun
	for _, rel := range importer.RulesFiles {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			continue
		}
		cands, err := importer.ImportRulesFile(root, rel, asNotes)
		if err != nil {
			return nil, err
		}
		if len(cands) > 0 {
			runs = append(runs, sourceRun{name: rel, candidates: cands})
		}
	}
	cands, warnings, err := importer.ImportCursorRules(root, asNotes)
	if err != nil {
		return nil, err
	}
	if len(cands) > 0 {
		runs = append(runs, sourceRun{name: ".cursor/rules", candidates: cands, warnings: warnings})
	}
	return runs, nil
}

// runImport is the single import code path (§D2.6): every source, `varve init`
// and `varve import` all land here, so batch identity, idempotency, undo and
// the report cannot diverge between entry points.
func runImport(cmd *cobra.Command, runs []sourceRun, flags importFlags) error {
	out := cmd.OutOrStdout()
	total := 0
	for _, r := range runs {
		total += len(r.candidates)
	}
	if total == 0 {
		fmt.Fprintln(out, "Nothing to import — no candidates found in the requested sources.")
		return nil
	}
	if !flags.yes && !flags.dryRun && !confirm(cmd, fmt.Sprintf(
		"Import %d entries from %d source(s)? Everything lands as proposed or notes. [y/N] ",
		total, len(runs))) {
		fmt.Fprintln(out, "Cancelled.")
		return nil
	}

	k, root, err := openKernel()
	if err != nil {
		return err
	}
	defer k.Close()

	batchID := util.GenerateID()
	summary := &lint.ImportSummary{
		Repo: filepath.Base(root), Sources: map[string]string{},
		Batch: batchID, DryRun: flags.dryRun,
	}
	for _, r := range runs {
		res, err := k.ImportBatchInto(batchID, r.name, toKernelCandidates(r.candidates), flags.dryRun)
		if err != nil {
			return err
		}
		summary.Sources[r.name] = fmt.Sprintf("%d entries", len(r.candidates))
		summary.Decisions += res.Decisions
		summary.Notes += res.Notes
		summary.Skipped += len(res.Skipped)
		summary.Warnings = append(summary.Warnings, r.warnings...)
		summary.Warnings = append(summary.Warnings, res.Errors...)
	}

	return printReport(cmd, k, root, summary, flags.format)
}

// toKernelCandidates converts source-shaped rows into kernel candidates. The
// kernel owns every lifecycle decision from here — the importer cannot ask for
// a status, and this function has no way to express one.
func toKernelCandidates(cands []importer.Candidate) []kernel.ImportCandidate {
	out := make([]kernel.ImportCandidate, 0, len(cands))
	for _, c := range cands {
		kc := kernel.ImportCandidate{
			SourceRef: c.SourceRef, AsDecision: c.AsDecision,
			Title: c.Title, Content: c.Content, Scope: c.Scope, Tags: c.Tags,
		}
		if c.AsDecision {
			kc.Kind = types.DecisionKind(c.Kind)
		}
		out = append(out, kc)
	}
	return out
}

// printReport runs the linter and prints §D5's report. Every non-dry import run
// ends here: the report is the product's front door, not an optional extra.
func printReport(cmd *cobra.Command, k *kernel.MemoryKernel, root string,
	summary *lint.ImportSummary, format string) error {

	db := k.Decisions().DB()
	opts := lint.Options{
		ProjectID: k.ProjectID(), RepoRoot: root, Now: time.Now().UTC(),
		CommitExists: lint.GitCommitExists(root),
		MarkExpired:  k.Decisions().MarkExpired,
	}
	res, err := lint.Run(db, opts)
	if err != nil {
		return err
	}
	backlog, err := lint.QueryBacklog(db, opts)
	if err != nil {
		return err
	}
	adoption, err := lint.QueryAdoption(db, opts)
	if err != nil {
		return err
	}
	rep := &lint.Report{
		GeneratedAt: opts.Now, Repo: filepath.Base(root),
		Import: summary, Lint: res, Backlog: backlog, Adoption: adoption,
	}
	if summary != nil {
		rep.Repo = summary.Repo
	}

	// §D6: the linter's own event, which gives the funnel its measurement for
	// free — score distribution and re-run frequency are a query over these.
	payload := map[string]any{
		"score":          res.Score.Value,
		"scored_entries": res.Entries,
		"findings":       findingCounts(res),
		"mode":           res.Modes,
		"gated_out":      res.GatedOut,
		"suppressed":     res.Score.Suppressed,
	}
	if err := k.RecordLintCompleted(payload); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	switch format {
	case "json":
		s, err := rep.JSON()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, s)
	case "md", "markdown":
		fmt.Fprint(out, rep.Markdown())
	default:
		fmt.Fprint(out, rep.Text())
	}
	return nil
}

func findingCounts(res *lint.Result) map[string]int {
	counts := map[string]int{}
	for _, c := range res.Checks {
		if c.NA {
			continue
		}
		counts[c.ID] = len(c.Findings)
	}
	return counts
}

func confirm(cmd *cobra.Command, prompt string) bool {
	in := cmd.InOrStdin()
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// ProbeAll lists what the known sources hold, without importing (§D2.1/§D2.6).
func ProbeAll(root string) []importer.Probe {
	probes := []importer.Probe{
		importer.ProbeClaudeMem(importer.DefaultClaudeMemDB()),
		importer.ProbeEngram(importer.DefaultEngramDB()),
	}
	probes = append(probes, importer.ProbeRules(root)...)
	var found []importer.Probe
	for _, p := range probes {
		if p.Available {
			found = append(found, p)
		}
	}
	return found
}
