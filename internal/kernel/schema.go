package kernel

// baselineV1SQL is migration 1: the recorded v1 baseline. It is the shape a v1
// database has once the old ad-hoc addColumnIfMissing calls have run (embedding
// and topic_key present), captured so that a fresh database is built by the
// migration framework rather than by ad-hoc DDL.
//
// It is frozen. The `memories` table is retired by the v2 tables (ADR-0001 D1)
// and its rows are carried over by MigrateFromV1; nothing new writes here.
const baselineV1SQL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);
INSERT OR IGNORE INTO schema_version (version) VALUES (1);

CREATE TABLE IF NOT EXISTS memories (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL CHECK (type IN ('decision', 'convention', 'fact', 'event')),
    content       TEXT NOT NULL,
    summary       TEXT,

    source        TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user', 'agent', 'git', 'import')),
    source_ref    TEXT,
    confidence    REAL NOT NULL DEFAULT 1.0 CHECK (confidence >= 0.0 AND confidence <= 1.0),

    project_id    TEXT NOT NULL,
    file_paths    TEXT NOT NULL DEFAULT '[]',
    tags          TEXT NOT NULL DEFAULT '[]',

    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'stale', 'archived')),
    superseded_by TEXT REFERENCES memories(id),

    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    accessed_at   TEXT,
    access_count  INTEGER NOT NULL DEFAULT 0,

    -- Column order is load-bearing: the v1 store reads with SELECT *, and in a
    -- real v1 database these two arrived via ALTER TABLE ADD COLUMN, so they
    -- sit last. The baseline must reproduce the recorded shape, not a tidier one.
    embedding     TEXT DEFAULT NULL,
    topic_key     TEXT DEFAULT NULL
);

CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
CREATE INDEX IF NOT EXISTS idx_memories_type    ON memories(type);
CREATE INDEX IF NOT EXISTS idx_memories_status  ON memories(status);
CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_topic_key
    ON memories(project_id, topic_key)
    WHERE topic_key IS NOT NULL;

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content,
    summary,
    tags,
    content=memories,
    content_rowid=rowid,
    tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content, summary, tags)
    VALUES (new.rowid, new.content, new.summary, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, summary, tags)
    VALUES ('delete', old.rowid, old.content, old.summary, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, summary, tags)
    VALUES ('delete', old.rowid, old.content, old.summary, old.tags);
    INSERT INTO memories_fts(rowid, content, summary, tags)
    VALUES (new.rowid, new.content, new.summary, new.tags);
END;
`
