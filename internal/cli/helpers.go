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
	return k, projectRoot, nil
}

// exitError prints an error to stderr and exits with code 1.
func exitError(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
