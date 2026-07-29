package kernel

// schemaV2SQL is ADR-0001 §D8's full v2 DDL, verbatim apart from the PRAGMAs
// (applied per connection in applyPragmas — PRAGMA journal_mode cannot run
// inside a transaction, and migrations run inside one) and the
// schema_migrations table (created by the migration framework itself, before
// any migration runs).
//
// This is migration 2. Migration 1 is the recorded v1 baseline.
const schemaV2SQL = `
-- ---------------------------------------------------------------- decisions
CREATE TABLE decisions (
    id                TEXT PRIMARY KEY,   -- ULID
    project_id        TEXT NOT NULL,
    kind              TEXT NOT NULL DEFAULT 'decision'
                      CHECK (kind IN ('decision','convention')),
    title             TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body              TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'proposed'
                      CHECK (status IN ('proposed','active','violated',
                                        'superseded','reverted','rejected')),
    scope             TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(scope)),
    confidence        REAL NOT NULL DEFAULT 1.0
                      CHECK (confidence >= 0.0 AND confidence <= 1.0),
    source            TEXT NOT NULL DEFAULT 'user'
                      CHECK (source IN ('user','agent','git','import','derived')),
    source_ref        TEXT,
    agent             TEXT,
    model             TEXT,
    session_id        TEXT,
    expires_at        TEXT,               -- RFC3339 UTC; NULL = never
    topic_key         TEXT,
    tags              TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
    supersedes        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(supersedes)),
    superseded_by     TEXT REFERENCES decisions(id),
    embedding         TEXT,               -- JSON float array; NULL = unembedded
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    decided_at        TEXT,
    status_changed_at TEXT NOT NULL,
    accessed_at       TEXT,
    access_count      INTEGER NOT NULL DEFAULT 0,
    CHECK (status NOT IN ('active','violated') OR decided_at IS NOT NULL),
    CHECK (status <> 'superseded' OR superseded_by IS NOT NULL)
);

CREATE INDEX idx_decisions_project_status ON decisions(project_id, status);
CREATE INDEX idx_decisions_expires
    ON decisions(expires_at) WHERE expires_at IS NOT NULL;
CREATE UNIQUE INDEX idx_decisions_topic_key
    ON decisions(project_id, topic_key)
    WHERE topic_key IS NOT NULL
      AND status IN ('proposed','active','violated');

-- Transition legality, enforced in DDL. The kernel enforces it too; this
-- trigger is the backstop against any writer that bypasses the kernel.
CREATE TRIGGER decisions_status_guard
BEFORE UPDATE OF status ON decisions
WHEN old.status <> new.status
 AND NOT (
      (old.status = 'proposed' AND new.status IN ('active','superseded','rejected'))
   OR (old.status = 'active'   AND new.status IN ('violated','superseded','reverted'))
   OR (old.status = 'violated' AND new.status IN ('active','superseded','reverted'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal decision status transition');
END;

-- Post-acceptance immutability of normative content (D3).
CREATE TRIGGER decisions_content_freeze
BEFORE UPDATE OF title, body, scope, kind ON decisions
WHEN old.status <> 'proposed'
 AND (old.title <> new.title OR old.body <> new.body
      OR old.scope <> new.scope OR old.kind <> new.kind)
BEGIN
    SELECT RAISE(ABORT, 'accepted decisions are immutable; supersede instead');
END;

CREATE VIRTUAL TABLE decisions_fts USING fts5(
    title, body, tags,
    content=decisions, content_rowid=rowid,
    tokenize='porter unicode61'
);
CREATE TRIGGER decisions_ai AFTER INSERT ON decisions BEGIN
    INSERT INTO decisions_fts(rowid, title, body, tags)
    VALUES (new.rowid, new.title, new.body, new.tags);
END;
CREATE TRIGGER decisions_ad AFTER DELETE ON decisions BEGIN
    INSERT INTO decisions_fts(decisions_fts, rowid, title, body, tags)
    VALUES ('delete', old.rowid, old.title, old.body, old.tags);
END;
CREATE TRIGGER decisions_au AFTER UPDATE ON decisions BEGIN
    INSERT INTO decisions_fts(decisions_fts, rowid, title, body, tags)
    VALUES ('delete', old.rowid, old.title, old.body, old.tags);
    INSERT INTO decisions_fts(rowid, title, body, tags)
    VALUES (new.rowid, new.title, new.body, new.tags);
END;

-- ----------------------------------------------------------------- evidence
CREATE TABLE evidence (
    id          TEXT PRIMARY KEY,         -- ULID
    decision_id TEXT NOT NULL REFERENCES decisions(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL
                CHECK (kind IN ('commit','pr','test','file','url','import')),
    ref         TEXT NOT NULL,            -- SHA / PR ref / test id / path / URL
    note        TEXT,
    added_by    TEXT NOT NULL CHECK (added_by IN ('human','agent','system')),
    -- 1 iff this row existed at the decision's proposed->active transition
    -- (set by the kernel in the acceptance transaction; D4/D6 revert rule).
    accepting   INTEGER NOT NULL DEFAULT 0 CHECK (accepting IN (0, 1)),
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_evidence_decision ON evidence(decision_id);
CREATE UNIQUE INDEX idx_evidence_dedupe ON evidence(decision_id, kind, ref);
-- Attribution needs "which decision has commit X as evidence" (D6):
CREATE INDEX idx_evidence_commit ON evidence(ref) WHERE kind = 'commit';
-- The decision-revert rule needs "is commit X *accepting* evidence" (D6):
CREATE INDEX idx_evidence_accepting_commit ON evidence(ref)
    WHERE kind = 'commit' AND accepting = 1;

-- -------------------------------------------------------------------- notes
CREATE TABLE notes (
    id           TEXT PRIMARY KEY,        -- ULID
    project_id   TEXT NOT NULL,
    content      TEXT NOT NULL,
    summary      TEXT,
    source       TEXT NOT NULL DEFAULT 'user'
                 CHECK (source IN ('user','agent','git','import')),
    source_ref   TEXT,
    confidence   REAL NOT NULL DEFAULT 1.0
                 CHECK (confidence >= 0.0 AND confidence <= 1.0),
    file_paths   TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(file_paths)),
    tags         TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
    status       TEXT NOT NULL DEFAULT 'active'
                 CHECK (status IN ('active','stale','archived')),
    topic_key    TEXT,
    embedding    TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    accessed_at  TEXT,
    access_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_notes_project_status ON notes(project_id, status);
CREATE UNIQUE INDEX idx_notes_topic_key
    ON notes(project_id, topic_key)
    WHERE topic_key IS NOT NULL AND status = 'active';

CREATE VIRTUAL TABLE notes_fts USING fts5(
    content, summary, tags,
    content=notes, content_rowid=rowid,
    tokenize='porter unicode61'
);
CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN
    INSERT INTO notes_fts(rowid, content, summary, tags)
    VALUES (new.rowid, new.content, new.summary, new.tags);
END;
CREATE TRIGGER notes_ad AFTER DELETE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, content, summary, tags)
    VALUES ('delete', old.rowid, old.content, old.summary, old.tags);
END;
CREATE TRIGGER notes_au AFTER UPDATE ON notes BEGIN
    INSERT INTO notes_fts(notes_fts, rowid, content, summary, tags)
    VALUES ('delete', old.rowid, old.content, old.summary, old.tags);
    INSERT INTO notes_fts(rowid, content, summary, tags)
    VALUES (new.rowid, new.content, new.summary, new.tags);
END;

-- ------------------------------------------------------------------- events
CREATE TABLE events (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,  -- total order
    id          TEXT NOT NULL UNIQUE,               -- ULID
    project_id  TEXT NOT NULL,
    ts          TEXT NOT NULL,                      -- RFC3339Nano UTC
    kind        TEXT NOT NULL,
    actor       TEXT NOT NULL CHECK (actor IN ('human','agent','system')),
    decision_id TEXT REFERENCES decisions(id),      -- blocks hard-delete of
                                                    -- any decision with history
    session_id  TEXT,
    agent       TEXT,
    model       TEXT,
    commit_sha  TEXT,
    payload     TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload))
);
CREATE INDEX idx_events_decision   ON events(decision_id, kind)
    WHERE decision_id IS NOT NULL;
CREATE INDEX idx_events_kind_ts    ON events(kind, ts);
CREATE INDEX idx_events_session    ON events(session_id)
    WHERE session_id IS NOT NULL;
CREATE INDEX idx_events_commit     ON events(commit_sha)
    WHERE commit_sha IS NOT NULL;
CREATE UNIQUE INDEX idx_events_expired_once
    ON events(decision_id) WHERE kind = 'decision.expired';
CREATE UNIQUE INDEX idx_events_scopematch_once
    ON events(decision_id, commit_sha) WHERE kind = 'diff.scope_match';
-- One observation per commit (ADR-0004 §D1's cursor predicate, made
-- airtight: the scan's "already observed" check is this index):
CREATE UNIQUE INDEX idx_events_observed_once
    ON events(commit_sha) WHERE kind = 'diff.observed';

CREATE TRIGGER events_append_only_u BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
CREATE TRIGGER events_append_only_d BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
`

// schemaV3SQL is migration 3 — ADR-0001 Amendment 1. The §D8 baseline above is
// deliberately not edited: applied migrations are frozen, and a new column
// arrives through the framework like any other change.
//
// `pending_topic_key` holds the topic_key a proposed successor claims but
// cannot yet hold, because a non-terminal predecessor still holds it. The key
// transfers to `topic_key` as the final step of the acceptance transaction,
// after the predecessors have been superseded and freed it (§D5, amended).
//
// The index is non-unique by design: competing proposals may legitimately pend
// the same key. The partial unique index on `topic_key` is unchanged and stays
// the backstop invariant.
//
// Mutual exclusion between `topic_key` and `pending_topic_key` is enforced in
// the kernel — SQLite's ALTER TABLE ADD COLUMN cannot add a cross-column
// CHECK, and rebuilding the table for one CHECK is not worth it.
//
// `pending_topic_key` is deliberately *not* added to decisions_content_freeze:
// it is editable while proposed (§D3), and after acceptance it is NULL.
const schemaV3SQL = `
ALTER TABLE decisions ADD COLUMN pending_topic_key TEXT;

-- Non-unique by design: competing proposals may pend the same key.
CREATE INDEX idx_decisions_pending_topic
    ON decisions(project_id, pending_topic_key)
    WHERE pending_topic_key IS NOT NULL AND status = 'proposed';

-- Backfill from the interim payload carrier, then nothing reads payloads.
UPDATE decisions
   SET pending_topic_key = (
        SELECT json_extract(e.payload, '$.topic_key')
          FROM events e
         WHERE e.decision_id = decisions.id
           AND e.kind = 'decision.proposed'
         ORDER BY e.seq LIMIT 1)
 WHERE status = 'proposed'
   AND topic_key IS NULL
   AND pending_topic_key IS NULL;
`

// schemaV4SQL is migration 4 — ADR-0001 Amendment 4. It licenses exactly one
// post-acceptance content write: purge's redaction shape.
//
// A trigger drop/recreate, not a table rebuild — SQLite cannot ALTER a
// trigger, but recreating one is cheap and touches no rows. The §D8 baseline
// and migrations 1–3 are frozen, as always.
//
// Why weaken the freeze at all: the store needs an answer to a secret pasted
// into a decision body, and for a row with events the only correct answer is
// redaction — the events are append-only and the id is load-bearing in
// attribution joins, so deleting the row is both barred by the FK and wrong.
// The exemption is the narrowest shape that admits the tombstone and nothing
// else: title exactly '[purged]', body empty, scope empty, kind unchanged.
//
// What the backstop loses, stated: a raw-SQL writer could now overwrite an
// accepted decision with that exact tombstone. It could already drop the
// trigger outright, so the guarantee this gives up is one it never had. The
// trigger's real job — catching accidental and semantic edits from inside the
// product — is unchanged, because no code path other than purge produces this
// shape.
const schemaV4SQL = `
DROP TRIGGER decisions_content_freeze;
CREATE TRIGGER decisions_content_freeze
BEFORE UPDATE OF title, body, scope, kind ON decisions
WHEN old.status <> 'proposed'
 AND (old.title <> new.title OR old.body <> new.body
      OR old.scope <> new.scope OR old.kind <> new.kind)
 AND NOT (new.title = '[purged]' AND new.body = ''
          AND new.scope = '[]' AND new.kind = old.kind)
BEGIN
    SELECT RAISE(ABORT, 'accepted decisions are immutable; supersede instead');
END;
`

// schemaV5SQL is migration 5 — ADR-0001 Amendment 5, executing §D7's
// pre-registered promotion after falsifier 6 fired.
//
// Three columns, not two: §D5.1 filters on `backfill` in the same hot join arm
// it reads `verdict` from, so promoting two of three would leave a JSON probe
// on the hot path and do half the job. Payload keys keep being written — they
// are the audit record and the export fidelity — but nothing queries them
// again.
//
// `committed_at` is stored as seconds-precision RFC3339 UTC Z-form. The
// backfill *normalizes* on the way in: SQLite's date parser accepts the
// local-offset forms a pre-`%cI`-fix row may carry and converts them, so a
// non-canonical legacy value cannot leak into the column. A value it cannot
// parse leaves the column NULL — the row becomes invisible to window joins,
// which it effectively already was, and `varve doctor` counts them.
//
// The append-only trigger is dropped and recreated **inside this migration's
// transaction**: a licensed one-time bypass, on the grounds that this is a
// representation move of facts that already exist — no fact changes, and no
// payload is rewritten. Migration 4's licensed redaction shape is the
// precedent. Outside this transaction the triggers are untouched.
const schemaV5SQL = `
ALTER TABLE events ADD COLUMN committed_at TEXT;
ALTER TABLE events ADD COLUMN verdict TEXT
    CHECK (verdict IS NULL OR verdict IN ('conform','violate'));
ALTER TABLE events ADD COLUMN backfill INTEGER NOT NULL DEFAULT 0
    CHECK (backfill IN (0, 1));

DROP TRIGGER events_append_only_u;

UPDATE events
   SET committed_at = strftime('%Y-%m-%dT%H:%M:%SZ',
                               json_extract(payload, '$.committed_at'))
 WHERE kind = 'diff.observed'
   AND json_extract(payload, '$.committed_at') IS NOT NULL;

UPDATE events
   SET verdict  = json_extract(payload, '$.verdict'),
       backfill = COALESCE(json_extract(payload, '$.backfill'), 0)
 WHERE kind = 'diff.scope_match';

CREATE TRIGGER events_append_only_u BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;

CREATE INDEX idx_events_committed
    ON events(committed_at)
    WHERE kind = 'diff.observed' AND committed_at IS NOT NULL;
`
