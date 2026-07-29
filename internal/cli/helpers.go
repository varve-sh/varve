package cli

import (
	"fmt"
	"os"

	"github.com/memtrace-dev/memtrace/internal/kernel"
	"github.com/memtrace-dev/memtrace/internal/types"
	"github.com/memtrace-dev/memtrace/internal/util"
)

// openKernel detects the project root from cwd and opens the memory kernel.
// The caller is responsible for calling kernel.Close().
func openKernel() (*kernel.MemoryKernel, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", err
	}

	projectRoot := util.FindProjectRoot(cwd)
	if projectRoot == "" {
		return nil, "", types.ErrNotInitialized
	}

	cfg := util.GetProjectConfig()
	entry, ok := cfg.Projects[projectRoot]
	if !ok {
		return nil, "", types.ErrNotInitialized
	}

	dbPath := util.GetProjectDbPath(projectRoot)
	k := kernel.New(dbPath, entry.ID)
	if err := k.Open(); err != nil {
		return nil, "", legacyDatabaseHint(err)
	}
	// A synthetic per-invocation session carrying agent='cli' (ADR-0004 §D3
	// amendment 3). It is registered, not announced: the session.started and
	// session.ended rows are written only if this invocation emits something
	// session-scoped, so `memtrace list` stays silent. Coverage denominators
	// exclude agent='cli' by definition, so these rows can never inflate the
	// kill-criterion metric — but without them a CLI recall is neither
	// attributable nor identifiable as CLI.
	k.SetSession(util.GenerateID(), kernel.SessionAgentCLI, "")
	return k, projectRoot, nil
}

// exitError prints an error to stderr and exits with code 1.
func exitError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
