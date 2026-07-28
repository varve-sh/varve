package scope

import (
	"errors"
	"testing"

	"github.com/memtrace-dev/memtrace/internal/types"
)

func TestValidatePattern(t *testing.T) {
	valid := []string{"src/**", "internal/kernel/*.go", "README.md", "**/*_test.go", "**"}
	for _, p := range valid {
		if err := ValidatePattern(p); err != nil {
			t.Errorf("ValidatePattern(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{"", "   ", "src/[unclosed"}
	for _, p := range invalid {
		err := ValidatePattern(p)
		if err == nil {
			t.Errorf("ValidatePattern(%q) = nil, want a validation error", p)
			continue
		}
		if !errors.Is(err, types.ErrValidation) {
			t.Errorf("ValidatePattern(%q) error does not wrap ErrValidation: %v", p, err)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		scope []string
		path  string
		want  bool
	}{
		{[]string{"internal/**"}, "internal/kernel/store.go", true},
		{[]string{"internal/*"}, "internal/kernel/store.go", false},
		{[]string{"internal/*/*.go"}, "internal/kernel/store.go", true},
		{[]string{"**/*_test.go"}, "internal/kernel/store_test.go", true},
		{[]string{"**/*_test.go"}, "store_test.go", true},
		{[]string{"README.md"}, "README.md", true},
		{[]string{"README.md"}, "docs/README.md", false},
		{[]string{"**"}, "anything/at/all.go", true},
		// An exact v1 file path is a valid glob matching exactly itself — the
		// property the v1→v2 scope carry-over relies on (D9 row mapping).
		{[]string{"internal/auth/middleware.go"}, "internal/auth/middleware.go", true},
		// Case-sensitive (D6).
		{[]string{"internal/**"}, "Internal/kernel/store.go", false},
		// Empty scope matches nothing, for either kind.
		{nil, "anything.go", false},
		{[]string{}, "anything.go", false},
		// Leading ./ and / are normalized away on both sides.
		{[]string{"./internal/**"}, "internal/x.go", true},
		{[]string{"internal/**"}, "./internal/x.go", true},
	}
	for _, c := range cases {
		if got := Matches(c.scope, c.path); got != c.want {
			t.Errorf("Matches(%v, %q) = %v, want %v", c.scope, c.path, got, c.want)
		}
	}
}

func TestMatchedGlobsAndFiles(t *testing.T) {
	sc := []string{"internal/**", "docs/*.md", "never/**"}
	files := []string{"internal/a.go", "internal/b/c.go", "docs/cli.md", "cmd/main.go"}

	globs := MatchedGlobs(sc, files)
	if len(globs) != 2 || globs[0] != "internal/**" || globs[1] != "docs/*.md" {
		t.Errorf("MatchedGlobs = %v, want [internal/** docs/*.md]", globs)
	}

	matched := MatchedFiles(sc, files)
	want := []string{"internal/a.go", "internal/b/c.go", "docs/cli.md"}
	if len(matched) != len(want) {
		t.Fatalf("MatchedFiles = %v, want %v", matched, want)
	}
	for i := range want {
		if matched[i] != want[i] {
			t.Errorf("MatchedFiles[%d] = %q, want %q", i, matched[i], want[i])
		}
	}
}
