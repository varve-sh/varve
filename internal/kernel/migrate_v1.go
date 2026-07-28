package kernel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/memtrace-dev/memtrace/internal/types"
)

// The v1→v2 conversion is an export→reimport, not an in-place table rebuild.
//
// EXPIRY CONDITION — read this before reusing anything below.
//
// This shortcut is licensed by exactly one assumption, recorded as Phase 0
// ruling 2 and restated normatively in ADR-0001 §D9: *every varve database in
// existence is founder-owned*. That is what makes it acceptable for the
// process to have intermediate states that are not crash-safe against
// third-party data.
//
// The assumption dies the moment there is a single external user. From that
// moment:
//
//	(a) every schema change, without exception, ships through the versioned
//	    framework in migrate.go as a transactional, backup-verified migration;
//	(b) if v2 has not shipped before the first external install, this file is
//	    void and the v1→v2 path must be reimplemented as a crash-safe in-place
//	    migration before release.
//
// The export→reimport shortcut must not be extended to any future version
// bump. Migrations 3+ go through migrate.go, always.
//
// NAMING — the paths below are still `.memtrace/`. ADR-0001 §D9 writes them as
// `.varve/`; the rename (.memtrace/ → .varve/, MEMTRACE_* → VARVE_*, module
// path) is a separate, later commit per the decisions log entry "Phase 1 open
// items closed", and this command is its natural boundary. Only the strings
// change when it lands.

// MigrateV1Options configures the conversion.
type MigrateV1Options struct {
	// DBPath is the live v1 database. On success it holds the new v2 database.
	DBPath string
	// BackupPath receives the v1 file. Default: <DBPath> with ".v1.bak.db".
	// Kept indefinitely; nothing deletes it.
	BackupPath string
	// ExportPath receives the intermediate JSON export. Default:
	// migration-v1-export.json beside the database.
	ExportPath string
	// ProjectID is used for the migration.completed event when the v1 rows
	// carry none.
	ProjectID string
}

// SkippedRow is a v1 row that did not survive the conversion, with the reason.
type SkippedRow struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// MigrationReport is the human-readable outcome of a v1→v2 conversion.
type MigrationReport struct {
	Exported    int          `json:"exported"`
	Decisions   int          `json:"decisions"`
	Notes       int          `json:"notes"`
	Evidence    int          `json:"evidence"`
	Skipped     []SkippedRow `json:"skipped"`
	NeedsTriage []string     `json:"needs_triage"` // v1 stale decisions, now proposed
	Unevidenced []string     `json:"unevidenced"`  // grandfathered active rows, D9
	ExportPath  string       `json:"export_path"`
	BackupPath  string       `json:"backup_path"`
	DBPath      string       `json:"db_path"`
}

// String renders the report for a terminal.
func (r *MigrationReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Migrated %s from schema v1 to v2.\n\n", r.DBPath)
	fmt.Fprintf(&b, "  exported   %d v1 rows\n", r.Exported)
	fmt.Fprintf(&b, "  decisions  %d\n", r.Decisions)
	fmt.Fprintf(&b, "  notes      %d\n", r.Notes)
	fmt.Fprintf(&b, "  evidence   %d\n", r.Evidence)
	fmt.Fprintf(&b, "  skipped    %d\n", len(r.Skipped))
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "             %s (%s): %s\n", s.ID, s.Type, s.Reason)
	}
	fmt.Fprintf(&b, "\n  v1 backup  %s  (kept — nothing deletes it)\n", r.BackupPath)
	fmt.Fprintf(&b, "  export     %s\n", r.ExportPath)
	if len(r.NeedsTriage) > 0 {
		fmt.Fprintf(&b, "\n%d stale v1 decisions came over as `proposed` and need re-confirmation.\n",
			len(r.NeedsTriage))
	}
	if len(r.Unevidenced) > 0 {
		fmt.Fprintf(&b, "%d active decisions arrived with no evidence (grandfathered). "+
			"The linter will flag them.\n", len(r.Unevidenced))
	}
	return b.String()
}

// MigrateFromV1 converts a v1 database to v2 by export→reimport (ADR-0001 §D9).
//
// It aborts and deletes nothing if the export count does not match the source
// row count, or if the reimport count does not match the export. ADR-0001
// falsifier 5: any count mismatch falsifies the framework.
func MigrateFromV1(opts MigrateV1Options) (*MigrationReport, error) {
	if !V2ReadPathsReady() {
		return nil, fmt.Errorf("%w: the v2 read paths (ADR-0001 §D10) are not wired up, so a "+
			"converted database would read as empty — every row would move to `decisions` and "+
			"`notes`, which nothing queries yet. Your database is untouched and keeps working "+
			"on the v1 read paths", types.ErrMigrationNotReady)
	}
	if opts.DBPath == "" {
		return nil, &types.ValidationError{Field: "db_path", Message: "must not be empty"}
	}
	dir := filepath.Dir(opts.DBPath)
	if opts.BackupPath == "" {
		opts.BackupPath = strings.TrimSuffix(opts.DBPath, ".db") + ".v1.bak.db"
	}
	if opts.ExportPath == "" {
		opts.ExportPath = filepath.Join(dir, "migration-v1-export.json")
	}

	// 1–2. Export read-only and verify the count against the source table.
	rows, embeddings, sourceCount, err := exportV1(opts.DBPath)
	if err != nil {
		// A previous run that died between moving the v1 file aside and
		// finishing the reimport leaves a partial v2 file here and the real
		// data in the backup. Say so, rather than "this is not a v1 database".
		if _, statErr := os.Stat(opts.BackupPath); statErr == nil {
			return nil, fmt.Errorf("%w\n\n"+
				"A v1 backup already exists at %s. An earlier conversion probably did not "+
				"finish: your data is in that file, and %s may be a partial v2 database. "+
				"Move the backup back over it before retrying",
				err, opts.BackupPath, opts.DBPath)
		}
		return nil, err
	}
	if len(rows) != sourceCount {
		return nil, fmt.Errorf(
			"aborting: exported %d rows but the v1 database holds %d — nothing was moved or deleted",
			len(rows), sourceCount)
	}
	blob, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("serialising export: %w", err)
	}
	if err := os.WriteFile(opts.ExportPath, blob, 0o644); err != nil {
		return nil, fmt.Errorf("writing export: %w", err)
	}

	// 3. Move the v1 file aside. The backup is kept indefinitely.
	if err := moveAsideV1(opts.DBPath, opts.BackupPath); err != nil {
		return nil, err
	}

	// 4. Fresh database at the original path, built by the migration framework.
	db, err := OpenDB(opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("creating v2 database: %w", err)
	}
	defer db.Close()
	if err := ApplySchema(db); err != nil {
		return nil, fmt.Errorf("applying v2 schema: %w", err)
	}

	// 5–6. Reimport, verify, and record the one event this path is allowed to
	// write (§D7: no synthetic per-row histories — we do not fabricate events
	// we did not observe).
	// The destination-side count check lives inside reimport's transaction, so
	// a mismatch aborts before anything is committed.
	return reimport(db, rows, embeddings, opts)
}

// exportV1 reads every v1 row, in every status and of every type, from a
// read-only connection. It returns the rows, their stored embeddings, and the
// source table's own count so the caller can verify one against the other.
//
// Embeddings travel beside the rows rather than inside them: §D9 pins the
// export artefact to the existing v1 JSON shape (types.Memory), which has no
// embedding field, while the row mapping requires embeddings to carry over
// (the content is unchanged, so the vectors stay valid). Both hold this way.
func exportV1(path string) ([]types.Memory, map[string]string, int, error) {
	db, err := sql.Open("sqlite", "file:"+escapeDSNPath(path)+"?_pragma=query_only(true)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, nil, 0, fmt.Errorf("opening v1 database: %w", err)
	}
	defer db.Close()

	legacy, err := isLegacyV1(db)
	if err != nil {
		return nil, nil, 0, err
	}
	if !legacy {
		return nil, nil, 0, fmt.Errorf("%s is not a v1 database (no `memories` table, or already migrated)", path)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&count); err != nil {
		return nil, nil, 0, fmt.Errorf("counting v1 rows: %w", err)
	}

	cursor, err := db.Query(`SELECT * FROM memories ORDER BY created_at, id`)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("reading v1 rows: %w", err)
	}
	defer cursor.Close()

	var out []types.Memory
	for cursor.Next() {
		m, err := scanMemory(cursor)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("scanning v1 row: %w", err)
		}
		out = append(out, *m)
	}
	if err := cursor.Err(); err != nil {
		return nil, nil, 0, err
	}

	embeddings := map[string]string{}
	embRows, err := db.Query(`SELECT id, embedding FROM memories WHERE embedding IS NOT NULL`)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("reading v1 embeddings: %w", err)
	}
	defer embRows.Close()
	for embRows.Next() {
		var id, emb string
		if err := embRows.Scan(&id, &emb); err != nil {
			return nil, nil, 0, err
		}
		embeddings[id] = emb
	}
	return out, embeddings, count, embRows.Err()
}

// moveAsideV1 checkpoints the WAL and renames the database and its sidecars.
func moveAsideV1(dbPath, backupPath string) error {
	db, err := OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("opening v1 database for checkpoint: %w", err)
	}
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err := db.Close(); err != nil {
		return fmt.Errorf("closing v1 database: %w", err)
	}

	if err := os.Rename(dbPath, backupPath); err != nil {
		return fmt.Errorf("moving the v1 database aside: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbPath + suffix); err == nil {
			_ = os.Rename(dbPath+suffix, backupPath+suffix)
		}
	}
	return nil
}

// reimport writes the exported rows into the fresh v2 database using the row
// mapping of §D9.
func reimport(db *sql.DB, rows []types.Memory, embeddings map[string]string, opts MigrateV1Options) (*MigrationReport, error) {
	report := &MigrationReport{
		Exported:   len(rows),
		ExportPath: opts.ExportPath,
		BackupPath: opts.BackupPath,
		DBPath:     opts.DBPath,
	}
	notes := NewNoteStore(db)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	projectID := opts.ProjectID
	// supersessions is applied in a second pass: superseded_by is an FK, so the
	// target row must exist first, and the row is inserted as `proposed` and
	// then transitioned (proposed→superseded is legal, so the status guard
	// stays honest even here).
	supersessions := map[string]string{}

	// Which v1 ids will become decisions at all. A v1 archived row whose
	// successor becomes a *note* has no decision to point at, so it takes
	// §D9's other branch — `rejected` — and must never enter the `proposed`
	// staging status, or an archived decision comes back as a live proposal.
	willBeDecision := make(map[string]bool, len(rows))
	for i := range rows {
		switch rows[i].Type {
		case types.MemoryTypeDecision, types.MemoryTypeConvention:
			willBeDecision[rows[i].ID] = true
		}
	}

	for i := range rows {
		m := &rows[i]
		if projectID == "" {
			projectID = m.ProjectID
		}
		switch m.Type {
		case types.MemoryTypeDecision, types.MemoryTypeConvention:
			n, skipped, err := insertMigratedDecision(tx, m, embeddings[m.ID], willBeDecision, supersessions, report)
			if err != nil {
				return nil, err
			}
			if skipped != nil {
				report.Skipped = append(report.Skipped, *skipped)
				continue
			}
			report.Evidence += n
			report.Decisions++
		default:
			note := &types.Note{
				ID:          m.ID,
				ProjectID:   m.ProjectID,
				Content:     m.Content,
				Summary:     m.Summary,
				Source:      m.Source,
				SourceRef:   m.SourceRef,
				Confidence:  m.Confidence,
				FilePaths:   m.FilePaths,
				Tags:        m.Tags,
				Status:      m.Status,
				TopicKey:    m.TopicKey,
				CreatedAt:   m.CreatedAt,
				UpdatedAt:   m.UpdatedAt,
				AccessedAt:  m.AccessedAt,
				AccessCount: m.AccessCount,
			}
			var noteEmbedding any
			if emb := embeddings[m.ID]; emb != "" {
				noteEmbedding = emb
			}
			if err := notes.insertRow(tx, note, noteEmbedding); err != nil {
				return nil, err
			}
			report.Notes++
		}
	}

	// Second pass: v1 archived decisions that named a decision successor. The
	// staging status is `proposed`, which is non-terminal, so every row entered
	// here must leave here — a row left staged is an archived decision
	// resurrected as a live proposal.
	now := time.Now().UTC()
	for id, successor := range supersessions {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM decisions WHERE id = ?`, successor).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			// The successor was skipped after all (e.g. untitled). §D9's other
			// branch applies: rejected, the disposal state for a v1 archive
			// with no usable successor. proposed→rejected is a legal
			// transition, so the status guard still holds.
			if _, err := tx.Exec(`
				UPDATE decisions SET status = 'rejected', status_changed_at = ? WHERE id = ?`,
				fmtTime(now), id); err != nil {
				return nil, fmt.Errorf("rejecting migrated decision %s: %w", id, err)
			}
			continue
		}
		if _, err := tx.Exec(`
			UPDATE decisions
			   SET status = 'superseded', superseded_by = ?, status_changed_at = ?
			 WHERE id = ?`, successor, fmtTime(now), id); err != nil {
			return nil, fmt.Errorf("superseding migrated decision %s: %w", id, err)
		}
	}

	// No row may be left in the staging status by accident. This is cheap and
	// it is the invariant F3 broke.
	var staged int
	if err := tx.QueryRow(`
		SELECT COUNT(*) FROM decisions
		 WHERE status = 'proposed' AND id IN (SELECT value FROM json_each(?))`,
		mustJSON(mapKeys(supersessions))).Scan(&staged); err != nil {
		return nil, err
	}
	if staged != 0 {
		return nil, fmt.Errorf(
			"aborting: %d archived v1 decisions were left in the `proposed` staging status; "+
				"the v1 backup at %s is intact", staged, opts.BackupPath)
	}

	if projectID == "" {
		projectID = "unknown"
	}
	if _, err := appendEvent(tx, EventInput{
		ProjectID: projectID,
		Kind:      types.EventMigrationDone,
		Actor:     types.ActorSystem,
		Payload: map[string]any{
			"from": 1, "to": 2,
			"decisions": report.Decisions,
			"notes":     report.Notes,
			"evidence":  report.Evidence,
			"skipped":   len(report.Skipped),
			"source_db": opts.BackupPath,
		},
	}); err != nil {
		return nil, err
	}

	// §D9 step 5, and falsifier 5: "any count mismatch between v1 export and v2
	// reimport". Counted against the destination tables, inside the transaction
	// and before commit — comparing the migration's own counters against each
	// other, which is what this check used to do, is arithmetically incapable
	// of failing and is worse than no check at all.
	var gotDecisions, gotNotes int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM decisions`).Scan(&gotDecisions); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&gotNotes); err != nil {
		return nil, err
	}
	if want := report.Exported - len(report.Skipped); gotDecisions+gotNotes != want {
		return nil, fmt.Errorf(
			"aborting: the v2 database holds %d decisions + %d notes = %d, but %d rows were "+
				"exported and %d skipped (expected %d); nothing was committed and the v1 backup "+
				"at %s is intact",
			gotDecisions, gotNotes, gotDecisions+gotNotes,
			report.Exported, len(report.Skipped), want, opts.BackupPath)
	}
	if gotDecisions != report.Decisions || gotNotes != report.Notes {
		return nil, fmt.Errorf(
			"aborting: the report claims %d decisions and %d notes but the database holds "+
				"%d and %d; the v1 backup at %s is intact",
			report.Decisions, report.Notes, gotDecisions, gotNotes, opts.BackupPath)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return report, nil
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// insertMigratedDecision writes one v1 decision/convention row. It returns the
// number of evidence rows created, or a non-nil SkippedRow if the row cannot
// be represented in v2.
func insertMigratedDecision(
	tx *sql.Tx, m *types.Memory, storedEmbedding string,
	willBeDecision map[string]bool, supersessions map[string]string, report *MigrationReport,
) (int, *SkippedRow, error) {
	title := strings.TrimSpace(m.Summary)
	if title == "" {
		title = truncate(strings.TrimSpace(m.Content), 120)
	}
	title = firstLine(title)
	if title == "" {
		return 0, &SkippedRow{ID: m.ID, Type: string(m.Type),
			Reason: "no summary and no content — a decision needs a title"}, nil
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}

	kind := types.DecisionKindDecision
	if m.Type == types.MemoryTypeConvention {
		kind = types.DecisionKindConvention
	}

	// §D9 status mapping. `superseded` is deferred to the second pass because
	// its FK target may not be inserted yet.
	var status types.DecisionStatus
	switch m.Status {
	case types.MemoryStatusActive:
		status = types.StatusActive
	case types.MemoryStatusStale:
		// Staleness demotes to "needs re-confirmation".
		status = types.StatusProposed
		report.NeedsTriage = append(report.NeedsTriage, m.ID)
	case types.MemoryStatusArchived:
		// §D9: `superseded_by` set in v1 → superseded; else → rejected. A
		// successor that becomes a *note* cannot be pointed at (the FK and the
		// superseded CHECK both need a decision), so it takes the else branch
		// rather than the staging path.
		if m.SupersededBy != "" && willBeDecision[m.SupersededBy] {
			status = types.StatusProposed // upgraded to superseded in pass two
			supersessions[m.ID] = m.SupersededBy
		} else {
			status = types.StatusRejected
		}
	default:
		return 0, &SkippedRow{ID: m.ID, Type: string(m.Type),
			Reason: "unknown v1 status " + string(m.Status)}, nil
	}

	source := types.DecisionSource(m.Source)
	switch source {
	case types.DecisionSourceUser, types.DecisionSourceAgent, types.DecisionSourceGit,
		types.DecisionSourceImport, types.DecisionSourceDerived:
	default:
		source = types.DecisionSourceImport
	}

	var decidedAt any
	if status == types.StatusActive {
		decidedAt = fmtTime(m.CreatedAt)
	}
	var embedding any
	// v1 embeddings stay valid: the content is carried over unchanged (§D9).
	if storedEmbedding != "" {
		embedding = storedEmbedding
	}

	if _, err := tx.Exec(`
		INSERT INTO decisions (`+decisionColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ProjectID, string(kind), title, m.Content, string(status),
		mustJSON(nonNilStrings(m.FilePaths)), m.Confidence, string(source),
		nullableString(m.SourceRef), nil, nil, nil,
		nil, nullableString(m.TopicKey), mustJSON(nonNilStrings(m.Tags)),
		"[]", nil, embedding,
		fmtTime(m.CreatedAt), fmtTime(m.UpdatedAt), decidedAt, fmtTime(m.UpdatedAt),
		nullableTime(m.AccessedAt), m.AccessCount,
		nil, // pending_topic_key: v1 has no deferred keys to carry
	); err != nil {
		return 0, nil, fmt.Errorf("inserting migrated decision %s: %w", m.ID, err)
	}

	// §D9: source_ref becomes the closest thing to accepting evidence a v1 row
	// has, so the D6 revert rule has something to fire on.
	evidence := 0
	if ref := strings.TrimSpace(m.SourceRef); ref != "" {
		in := EvidenceInput{Kind: types.EvidenceKindImport, Ref: ref, AddedBy: types.ActorSystem}
		if sha, ok := strings.CutPrefix(ref, "git:"); ok && sha != "" {
			in = EvidenceInput{Kind: types.EvidenceKindCommit, Ref: sha, AddedBy: types.ActorSystem}
		}
		if _, err := insertEvidence(tx, m.ID, in, true, m.CreatedAt); err != nil {
			return 0, nil, err
		}
		evidence = 1
	}
	if evidence == 0 && status == types.StatusActive {
		// Grandfather clause: the evidence requirement gates the proposed→active
		// transition, which this row never makes. The linter flags it.
		report.Unevidenced = append(report.Unevidenced, m.ID)
	}
	return evidence, nil, nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
