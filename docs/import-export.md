# Import & Export

`varve export` dumps memories to a file. `varve import` loads them back. Both support JSON and Markdown.

---

## Export

```bash
# JSON (default)
varve export --output memories.json

# Markdown — human-readable, editable by hand
varve export --format markdown --output memories.md

# Filtered export
varve export --type decision --output decisions.json
varve export --status stale --output stale.json
```

---

## Import

```bash
# Auto-detected by file extension
varve import memories.md
varve import memories.json

# Preview without saving
varve import memories.md --dry-run

# Import only decisions
varve import memories.json --type decision

# Force format
varve import backup.txt --format json
```

---

## Markdown format

The Markdown export format uses `## [type] first line` headings with a metadata list block, separated by `---`. It is readable as-is and editable before reimporting.

```markdown
## [decision] We use JWT with RS256 — stateless API, no session storage

- Tags: auth, security
- Confidence: 1.00
- Created: 2026-03-22T10:00:00Z
- Files: src/middleware/auth.go

We use JWT with RS256 for authentication. The API is completely stateless — no session
storage anywhere in the system. Access tokens expire after 1 hour, refresh tokens after 30 days.

---

## [convention] Error handling: always wrap with fmt.Errorf

- Tags: go, errors
- Confidence: 0.95
- Created: 2026-03-20T08:00:00Z

All errors must be wrapped with fmt.Errorf("context: %w", err) so they are inspectable
with errors.Is / errors.As at the call site.
```

---

## Importing from a URL

Both commands accept an HTTP/HTTPS URL in place of a file path:

```bash
varve import https://example.com/memories.json
```

---

## Round-trip example

```bash
# Export from project A
cd project-a
varve export --format markdown --output ../shared-conventions.md

# Import into project B
cd ../project-b
varve import ../shared-conventions.md --dry-run   # preview first
varve import ../shared-conventions.md
```
