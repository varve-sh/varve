package importer

import (
	"os"
	"path/filepath"
	"testing"
)

// The probe and the importer have to agree about what is importable, because
// init prints the probe's count and then tells the user to run the importer.
// They did not: init writes varve's own instruction block into CLAUDE.md, the
// probe counted it, and the clean-room first run read
//
//	Found existing memory:
//	  CLAUDE.md        1 block
//	Import it with: varve import claude-mem | engram | rules
//	$ varve import rules
//	Nothing to import — no candidates found in the requested sources.
//
// which is the product's opening sequence contradicting itself over a file it
// wrote itself.
func TestProbeRules_DoesNotOfferOurOwnInstructionBlock(t *testing.T) {
	root := t.TempDir()
	ours := "\n## varve (memory)\n\nUse memory_pack before touching files, then memory_recall.\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(ours), 0o644); err != nil {
		t.Fatal(err)
	}

	// Guard: if the importer ever stopped skipping the block, this test would
	// be asserting agreement with a broken importer rather than agreement.
	cands, err := ImportRulesFile(root, "CLAUDE.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatalf("the importer offered %d candidates for our own block, so the probe "+
			"has nothing to agree with", len(cands))
	}
	if got := ProbeRules(root); len(got) != 0 {
		t.Errorf("probe announced %+v — init would tell the user to import a block "+
			"that `varve import rules` refuses", got)
	}
}

// The filter must not swallow the user's actual rules, which is the whole
// point of the source.
func TestProbeRules_CountsTheUsersOwnBlocks(t *testing.T) {
	root := t.TempDir()
	content := "## Use pnpm\n\nnpm lockfiles drift.\n" +
		"\n## varve (memory)\n\nUse memory_pack before touching files.\n" +
		"\n## Review everything\n\nTwo approvals.\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ProbeRules(root)
	if len(got) != 1 {
		t.Fatalf("probe returned %d sources, want 1: %+v", len(got), got)
	}
	cands, err := ImportRulesFile(root, "CLAUDE.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Count != len(cands) {
		t.Errorf("probe counts %d blocks, importer produces %d candidates — the number "+
			"init prints is not the number the import delivers", got[0].Count, len(cands))
	}
	if got[0].Count != 2 {
		t.Errorf("probe counts %d, want the 2 blocks the user wrote", got[0].Count)
	}
}
