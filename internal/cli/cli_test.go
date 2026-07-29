package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/varve-sh/varve/internal/kernel"
	"github.com/varve-sh/varve/internal/types"
	"github.com/varve-sh/varve/internal/util"
)

// isolateConfig points the global config at a temp directory for the duration of
// a test.
//
// Setting HOME alone is not enough, and CI proved it: os.UserConfigDir() returns
// $HOME/Library/Application Support on macOS but prefers $XDG_CONFIG_HOME on
// Linux. On a runner with XDG_CONFIG_HOME set, every test that believed it had
// isolated its config was reading and writing the runner's real one — the whole
// suite shared a single config file and saw each other's projects. Invisible on
// a Mac, which is why it shipped. Both variables are set so the isolation holds
// on either platform.
func isolateConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

// setupProject creates a temp directory that looks like an initialized varve
// project. It returns the project root and a kernel pre-populated with the
// provided memories. The test's cwd is changed to the project root for the
// duration of the test.
func setupProject(t *testing.T, memories ...types.MemorySaveInput) (*kernel.MemoryKernel, string) {
	t.Helper()

	isolateConfig(t)

	root := t.TempDir()

	// Resolve symlinks so the key matches what os.Getwd() returns after chdir
	// (on macOS /var/folders/... symlinks to /private/var/folders/...)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = realRoot

	// Create .varve directory
	if err := os.MkdirAll(filepath.Join(root, ".varve"), 0755); err != nil {
		t.Fatal(err)
	}

	// Register the project in the config
	projectID := util.GenerateID()
	cfg := util.GetProjectConfig()
	cfg.Projects[root] = util.ProjectEntry{
		ID:        projectID,
		Name:      filepath.Base(root),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := util.SaveProjectConfig(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Open kernel (creates the DB and applies schema)
	dbPath := util.GetProjectDbPath(root)
	k := kernel.New(dbPath, projectID)
	if err := k.Open(); err != nil {
		t.Fatalf("open kernel: %v", err)
	}
	t.Cleanup(func() { k.Close() })

	for _, m := range memories {
		if _, _, err := k.Save(m); err != nil {
			t.Fatalf("pre-populate save: %v", err)
		}
	}

	// Change cwd to the project root so openKernel() resolves correctly
	orig, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	return k, root
}

// runCmdStdin is runCmd with something on stdin, for the commands that ask a
// question — today only `decision purge`, whose typed confirmation is part of
// its contract.
func runCmdStdin(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	return runCmdWith(t, strings.NewReader(stdin), args...)
}

// runCmd executes a root cobra command with the given args and returns stdout.
// CLI commands use fmt.Printf directly so we redirect os.Stdout via a pipe.
// These tests must not run in parallel (they share os.Stdout and os.Chdir).
func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCmdWith(t, strings.NewReader(""), args...)
}

func runCmdWith(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	// Colored output goes through color.Output, which is bound to the real
	// stdout at package init — without redirecting it too, every line printed
	// through a *color.Color is invisible to these assertions, which is how a
	// suite ends up asserting on the few uncolored lines instead of on the
	// outcome.
	origColor := color.Output
	color.Output = w
	t.Cleanup(func() { os.Stdout = origStdout; color.Output = origColor })

	cmd := NewRootCmd()
	cmd.SetArgs(args)
	cmd.SetIn(stdin)
	runErr := cmd.Execute()

	w.Close()
	os.Stdout = origStdout

	var sb strings.Builder
	io.Copy(&sb, r)
	r.Close()

	return sb.String(), runErr
}

// --- save ---

func TestSaveCmd_Basic(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "save", "We use PostgreSQL as the primary database")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Saved memory") {
		t.Errorf("expected confirmation, got: %s", out)
	}
}

func TestSaveCmd_WithFlags(t *testing.T) {
	_, _ = setupProject(t)

	out, err := runCmd(t, "save", "Auth uses JWT RS256",
		"--type", "decision",
		"--tags", "auth,security",
		"--files", "src/auth.go",
		"--confidence", "0.9",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "decision") {
		t.Errorf("expected type in output, got: %s", out)
	}
}

func TestSaveCmd_NotInitialized(t *testing.T) {
	isolateConfig(t)

	// Temp dir with no .varve/ and no .git/
	bare := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(bare)
	t.Cleanup(func() { os.Chdir(orig) })

	_, err := runCmd(t, "save", "something")
	if err == nil {
		t.Error("expected error when not initialized")
	}
}

// --- search ---

func TestSearchCmd_FindsResult(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "Redis is used for session caching", Tags: []string{"cache"}},
	)

	out, err := runCmd(t, "search", "Redis caching")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "Redis") {
		t.Errorf("expected Redis in output, got: %s", out)
	}
}

func TestSearchCmd_NoResults(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "search", "quantum entanglement xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No memories found") {
		t.Errorf("expected no-results message, got: %s", out)
	}
}

func TestSearchCmd_JSONOutput(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "We deploy to AWS us-east-1"},
	)

	out, err := runCmd(t, "search", "AWS", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var results []interface{}
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &results); jsonErr != nil {
		t.Errorf("expected valid JSON, got: %s\nerror: %v", out, jsonErr)
	}
}

func TestSearchCmd_TypeFilter(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "Kubernetes deployment event", Type: types.MemoryTypeEvent},
		types.MemorySaveInput{Content: "Kubernetes naming convention", Type: types.MemoryTypeConvention},
	)

	out, err := runCmd(t, "search", "Kubernetes", "--type", "convention")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "deployment event") {
		t.Errorf("type filter should exclude events, got: %s", out)
	}
	if !strings.Contains(out, "naming convention") {
		t.Errorf("expected convention memory, got: %s", out)
	}
}

// --- list ---

func TestListCmd_Basic(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "memory one", Type: types.MemoryTypeDecision},
		types.MemorySaveInput{Content: "memory two", Type: types.MemoryTypeFact},
	)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "memory one") || !strings.Contains(out, "memory two") {
		t.Errorf("expected both memories in output, got: %s", out)
	}
}

func TestListCmd_TypeFilter(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "a decision", Type: types.MemoryTypeDecision},
		types.MemorySaveInput{Content: "a fact", Type: types.MemoryTypeFact},
	)

	out, err := runCmd(t, "list", "--type", "decision")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "a decision") {
		t.Errorf("expected decision memory, got: %s", out)
	}
	if strings.Contains(out, "a fact") {
		t.Errorf("type filter should exclude facts, got: %s", out)
	}
}

func TestListCmd_Empty(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "list")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No memories found") {
		t.Errorf("expected no-results message, got: %s", out)
	}
}

func TestListCmd_JSONOutput(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "some memory"},
	)

	out, err := runCmd(t, "list", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result interface{}
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); jsonErr != nil {
		t.Errorf("expected valid JSON, got: %s\nerror: %v", out, jsonErr)
	}
}

// --- rm ---

func TestRmCmd_ByFullID(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "to be deleted"})

	out, err := runCmd(t, "rm", mem.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Deleted") {
		t.Errorf("expected deletion confirmation, got: %s", out)
	}

	got, _ := k.Get(mem.ID)
	if got != nil {
		t.Error("memory should be deleted")
	}
}

func TestRmCmd_ByPrefix(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "prefix deletion test"})
	prefix := mem.ID[:8]

	out, err := runCmd(t, "rm", prefix)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Deleted") {
		t.Errorf("expected deletion confirmation, got: %s", out)
	}
}

func TestRmCmd_NotFound(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "rm", "01NONEXISTENTID0000000000X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not-found message, got: %s", out)
	}
}

// --- update ---

func TestUpdateCmd_Content(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "original content"})

	out, err := runCmd(t, "update", mem.ID, "--content", "updated content")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Updated") {
		t.Errorf("expected update confirmation, got: %s", out)
	}

	got, _ := k.Get(mem.ID)
	if got.Content != "updated content" {
		t.Errorf("want 'updated content', got %q", got.Content)
	}
}

func TestUpdateCmd_Tags(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "some memory", Tags: []string{"old"}})

	_, err := runCmd(t, "update", mem.ID, "--tags", "new,tag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := k.Get(mem.ID)
	if len(got.Tags) != 2 || got.Tags[0] != "new" {
		t.Errorf("expected tags [new tag], got %v", got.Tags)
	}
}

func TestUpdateCmd_NotFound(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "update", "01NONEXISTENTID0000000000X", "--content", "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not-found message, got: %s", out)
	}
}

// --- export ---

func TestExportCmd_Stdout(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "export test memory"},
	)

	out, err := runCmd(t, "export")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result interface{}
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); jsonErr != nil {
		t.Errorf("expected valid JSON, got: %s\nerror: %v", out, jsonErr)
	}
}

func TestExportCmd_ToFile(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "export to file test"},
	)

	outFile := filepath.Join(t.TempDir(), "memories.json")
	out, err := runCmd(t, "export", "--output", outFile)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Exported") {
		t.Errorf("expected export confirmation, got: %s", out)
	}

	data, readErr := os.ReadFile(outFile)
	if readErr != nil {
		t.Fatalf("output file not created: %v", readErr)
	}
	var result interface{}
	if jsonErr := json.Unmarshal(data, &result); jsonErr != nil {
		t.Errorf("invalid JSON in output file: %v", jsonErr)
	}
}

func TestExportCmd_TypeFilter(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "decision memory", Type: types.MemoryTypeDecision},
		types.MemorySaveInput{Content: "fact memory", Type: types.MemoryTypeFact},
	)

	out, err := runCmd(t, "export", "--type", "decision")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "fact memory") {
		t.Errorf("type filter should exclude facts, got: %s", out)
	}
	if !strings.Contains(out, "decision memory") {
		t.Errorf("expected decision memory in export, got: %s", out)
	}
}

// --- import ---

func TestImportCmd_FromFile(t *testing.T) {
	setupProject(t)

	data := `[{"content":"imported decision","type":"decision"},{"content":"imported fact","type":"fact"}]`
	f := filepath.Join(t.TempDir(), "memories.json")
	os.WriteFile(f, []byte(data), 0644)

	out, err := runCmd(t, "import", f)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Imported 2") {
		t.Errorf("expected 'Imported 2', got: %s", out)
	}
}

func TestImportCmd_DryRun(t *testing.T) {
	setupProject(t)

	data := `[{"content":"memory one","type":"fact"},{"content":"memory two","type":"decision"}]`
	f := filepath.Join(t.TempDir(), "memories.json")
	os.WriteFile(f, []byte(data), 0644)

	out, err := runCmd(t, "import", f, "--dry-run")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Would import 2") {
		t.Errorf("expected dry-run summary, got: %s", out)
	}
}

func TestImportCmd_TypeFilter(t *testing.T) {
	setupProject(t)

	data := `[{"content":"a decision","type":"decision"},{"content":"a fact","type":"fact"}]`
	f := filepath.Join(t.TempDir(), "memories.json")
	os.WriteFile(f, []byte(data), 0644)

	out, err := runCmd(t, "import", f, "--type", "decision")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Imported 1") {
		t.Errorf("expected 'Imported 1', got: %s", out)
	}
}

func TestImportCmd_EmptyFile(t *testing.T) {
	setupProject(t)

	f := filepath.Join(t.TempDir(), "empty.json")
	os.WriteFile(f, []byte(`[]`), 0644)

	out, err := runCmd(t, "import", f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No memories") {
		t.Errorf("expected no-memories message, got: %s", out)
	}
}

func TestImportCmd_FileNotFound(t *testing.T) {
	setupProject(t)

	_, err := runCmd(t, "import", "/nonexistent/path.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// --- edit ---

func TestEditCmd_UpdatesContent(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "original content"})

	// Use a fake editor that overwrites the file with new content
	fakeEditor := filepath.Join(t.TempDir(), "fake-editor.sh")
	if err := os.WriteFile(fakeEditor, []byte("#!/bin/sh\nprintf 'edited content' > \"$1\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", fakeEditor)

	out, err := runCmd(t, "edit", mem.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Updated") {
		t.Errorf("expected update confirmation, got: %s", out)
	}

	got, _ := k.Get(mem.ID)
	if got.Content != "edited content" {
		t.Errorf("want 'edited content', got %q", got.Content)
	}
}

func TestEditCmd_NoChange(t *testing.T) {
	k, _ := setupProject(t)

	mem, _, _ := k.Save(types.MemorySaveInput{Content: "unchanged content"})

	// Editor that leaves the file unchanged
	fakeEditor := filepath.Join(t.TempDir(), "noop-editor.sh")
	if err := os.WriteFile(fakeEditor, []byte("#!/bin/sh\n# no-op\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", fakeEditor)

	out, err := runCmd(t, "edit", mem.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("expected 'No changes' message, got: %s", out)
	}
}

func TestEditCmd_NotFound(t *testing.T) {
	setupProject(t)

	fakeEditor := filepath.Join(t.TempDir(), "fake-editor.sh")
	os.WriteFile(fakeEditor, []byte("#!/bin/sh\n"), 0755)
	t.Setenv("EDITOR", fakeEditor)

	out, err := runCmd(t, "edit", "01NONEXISTENTID0000000000X")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not-found message, got: %s", out)
	}
}

// --- status ---

func TestStatusCmd_Basic(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "d1", Type: types.MemoryTypeDecision},
		types.MemorySaveInput{Content: "f1", Type: types.MemoryTypeFact},
	)

	out, err := runCmd(t, "status")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	// "Status:" line is printed via fmt.Printf (not color), so it lands in the buffer
	if !strings.Contains(out, "Status:") {
		t.Errorf("expected 'Status:' line in output, got: %s", out)
	}
}

func TestStatusCmd_JSONOutput(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "some memory"},
	)

	out, err := runCmd(t, "status", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	var result map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); jsonErr != nil {
		t.Errorf("expected valid JSON, got: %s\nerror: %v", out, jsonErr)
	}
	if _, ok := result["total"]; !ok {
		t.Error("expected 'total' field in JSON output")
	}
}

// --- config ---

func TestConfigSetCmd_EmbedKey(t *testing.T) {
	setupProject(t)

	out, err := runCmd(t, "config", "set", "embed.key", "sk-test-123")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "embed.key") {
		t.Errorf("expected confirmation, got: %s", out)
	}

	cfg := util.GetProjectConfig()
	if cfg.Embed.Key != "sk-test-123" {
		t.Errorf("want sk-test-123, got %q", cfg.Embed.Key)
	}
}

func TestConfigSetCmd_EmbedModel(t *testing.T) {
	setupProject(t)
	_, err := runCmd(t, "config", "set", "embed.model", "nomic-embed-text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if util.GetProjectConfig().Embed.Model != "nomic-embed-text" {
		t.Errorf("model not persisted")
	}
}

func TestConfigSetCmd_EmbedURL(t *testing.T) {
	setupProject(t)
	_, err := runCmd(t, "config", "set", "embed.url", "http://localhost:11434/v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if util.GetProjectConfig().Embed.URL != "http://localhost:11434/v1" {
		t.Errorf("url not persisted")
	}
}

func TestConfigSetCmd_InvalidKey(t *testing.T) {
	setupProject(t)
	_, err := runCmd(t, "config", "set", "unknown.key", "value")
	if err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestConfigUnsetCmd(t *testing.T) {
	setupProject(t)
	runCmd(t, "config", "set", "embed.key", "to-be-removed")
	_, err := runCmd(t, "config", "unset", "embed.key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if util.GetProjectConfig().Embed.Key != "" {
		t.Error("expected embed.key to be cleared")
	}
}

func TestConfigGetCmd(t *testing.T) {
	setupProject(t)
	runCmd(t, "config", "set", "embed.key", "sk-show-me")
	runCmd(t, "config", "set", "embed.model", "ada-002")

	out, err := runCmd(t, "config", "get")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "embed.key") {
		t.Errorf("expected embed.key in output, got: %s", out)
	}
	if !strings.Contains(out, "embed.model") {
		t.Errorf("expected embed.model in output, got: %s", out)
	}
}

// --- reindex ---

func TestReindexCmd_NoEmbedder(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "memory without embedder"},
	)
	t.Setenv("VARVE_EMBED_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VARVE_EMBED_URL", "")
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	// Output goes to stderr for the "no embedder" message; runCmd captures stdout.
	// We just verify the command exits without error and stdout is empty/benign.
	out, err := runCmd(t, "reindex")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
}

func TestReindexCmd_WithEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		json.NewEncoder(w).Encode(resp{Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{Embedding: []float64{0.1, 0.2, 0.3}}}})
	}))
	defer srv.Close()

	// Save memories *before* setting the embed key so the kernel opens without
	// an embedder and memories are stored with embedding = NULL.
	setupProject(t,
		types.MemorySaveInput{Content: "needs embedding one"},
		types.MemorySaveInput{Content: "needs embedding two"},
	)

	// Enable the embedder for the reindex run.
	t.Setenv("VARVE_EMBED_PROVIDER", "")
	t.Setenv("VARVE_EMBED_KEY", "test-key")
	t.Setenv("VARVE_EMBED_URL", srv.URL)

	out, err := runCmd(t, "reindex")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Reindexed") {
		t.Errorf("expected 'Reindexed' in output, got: %s", out)
	}
}

// --- doctor ---

func TestDoctorCmd_AllOK(t *testing.T) {
	_, root := setupProject(t,
		types.MemorySaveInput{Content: "We use JWT", Type: types.MemoryTypeDecision},
	)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	// Write a CLAUDE.md with varve instructions.
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Use memory_save to store decisions."), 0644)
	// Write a .claude/mcp.json referencing varve.
	os.MkdirAll(filepath.Join(root, ".claude"), 0755)
	os.WriteFile(filepath.Join(root, ".claude", "mcp.json"), []byte(`{"mcpServers":{"varve":{"command":"varve","args":["serve"]}}}`), 0644)

	out, err := runCmd(t, "doctor")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "[ok]") {
		t.Errorf("expected [ok] checks, got: %s", out)
	}
	if strings.Contains(out, "[warn]") || strings.Contains(out, "[fail]") {
		t.Errorf("expected no warnings or failures, got: %s", out)
	}
	if !strings.Contains(out, "Everything looks good") {
		t.Errorf("expected success summary, got: %s", out)
	}
}

func TestDoctorCmd_StaleWarning(t *testing.T) {
	_, root := setupProject(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	// Write CLAUDE.md and MCP config to avoid those warnings.
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("memory_save"), 0644)
	os.MkdirAll(filepath.Join(root, ".claude"), 0755)
	os.WriteFile(filepath.Join(root, ".claude", "mcp.json"), []byte(`{"mcpServers":{"varve":{}}}`), 0644)

	// Create a memory referencing a file, then delete the file so scan marks it stale.
	tmpFile := filepath.Join(root, "tmp.go")
	os.WriteFile(tmpFile, []byte("package main"), 0644)
	k, _, _ := func() (*kernel.MemoryKernel, string, error) {
		orig, _ := os.Getwd()
		os.Chdir(root)
		defer os.Chdir(orig)
		return openKernel()
	}()
	if k != nil {
		k.Save(types.MemorySaveInput{
			Content:   "Temp file convention",
			FilePaths: []string{"tmp.go"},
		})
		os.Remove(tmpFile)
		k.ScanStaleness(root)
		k.Close()
	}

	out, err := runCmd(t, "doctor")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Stale") {
		t.Errorf("expected stale warning, got: %s", out)
	}
}

func TestDoctorCmd_MissingMCPConfig(t *testing.T) {
	_, root := setupProject(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("memory_save"), 0644)
	// No MCP config files.

	out, err := runCmd(t, "doctor")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "MCP config") {
		t.Errorf("expected MCP config warning, got: %s", out)
	}
}

func TestDoctorCmd_MissingCLAUDEMD(t *testing.T) {
	_, root := setupProject(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")
	os.MkdirAll(filepath.Join(root, ".claude"), 0755)
	os.WriteFile(filepath.Join(root, ".claude", "mcp.json"), []byte(`{"mcpServers":{"varve":{}}}`), 0644)
	// No CLAUDE.md.

	out, err := runCmd(t, "doctor")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "CLAUDE.md") {
		t.Errorf("expected CLAUDE.md warning, got: %s", out)
	}
}

// --- stats ---

func TestStatsCmd_Basic(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "We use JWT", Type: types.MemoryTypeDecision},
		types.MemorySaveInput{Content: "Error handling convention", Type: types.MemoryTypeConvention},
	)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	out, err := runCmd(t, "stats")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "Live memories") {
		t.Errorf("expected 'Live memories', got: %s", out)
	}
	if !strings.Contains(out, "Saved this period") {
		t.Errorf("expected 'Saved this period', got: %s", out)
	}
	if !strings.Contains(out, "Recalls this period") {
		t.Errorf("expected 'Recalls this period', got: %s", out)
	}
}

func TestStatsCmd_JSON(t *testing.T) {
	setupProject(t,
		types.MemorySaveInput{Content: "We use PostgreSQL", Type: types.MemoryTypeFact},
	)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	out, err := runCmd(t, "stats", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if _, ok := result["total_live"]; !ok {
		t.Errorf("expected 'total_live' in JSON, got: %s", out)
	}
	if _, ok := result["recalls_this_period"]; !ok {
		t.Errorf("expected 'recalls_this_period' in JSON, got: %s", out)
	}
}

func TestStatsCmd_CustomDays(t *testing.T) {
	setupProject(t)
	t.Setenv("VARVE_EMBED_PROVIDER", "disabled")

	out, err := runCmd(t, "stats", "--days", "30")
	if err != nil {
		t.Fatalf("unexpected error: %v\noutput: %s", err, out)
	}
	if !strings.Contains(out, "last 30 days") {
		t.Errorf("expected 'last 30 days', got: %s", out)
	}
}
