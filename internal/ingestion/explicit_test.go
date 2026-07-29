package ingestion

import (
	"errors"
	"testing"

	"github.com/varve-sh/varve/internal/types"
)

func TestValidate_Valid(t *testing.T) {
	input := &types.MemorySaveInput{
		Content:    "valid content",
		Type:       types.MemoryTypeDecision,
		Confidence: 0.8,
		Tags:       []string{"auth", "api"},
		FilePaths:  []string{"src/main.go"},
	}
	if err := Validate(input); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_EmptyContent(t *testing.T) {
	err := Validate(&types.MemorySaveInput{Content: ""})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation, got %v", err)
	}
}

func TestValidate_ContentTooLong(t *testing.T) {
	long := make([]byte, 10001)
	for i := range long {
		long[i] = 'x'
	}
	err := Validate(&types.MemorySaveInput{Content: string(long)})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation for long content, got %v", err)
	}
}

func TestValidate_InvalidType(t *testing.T) {
	err := Validate(&types.MemorySaveInput{
		Content: "valid",
		Type:    "invalid-type",
	})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation for bad type, got %v", err)
	}
}

func TestValidate_ConfidenceOutOfRange(t *testing.T) {
	err := Validate(&types.MemorySaveInput{
		Content:    "valid",
		Confidence: 1.5,
	})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation for confidence > 1.0, got %v", err)
	}
}

func TestValidate_TooManyTags(t *testing.T) {
	tags := make([]string, 21)
	for i := range tags {
		tags[i] = "tag"
	}
	err := Validate(&types.MemorySaveInput{Content: "valid", Tags: tags})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation for >20 tags, got %v", err)
	}
}

func TestValidate_AbsoluteFilePath(t *testing.T) {
	err := Validate(&types.MemorySaveInput{
		Content:   "valid",
		FilePaths: []string{"/absolute/path.go"},
	})
	if !errors.Is(err, types.ErrValidation) {
		t.Errorf("want ErrValidation for absolute path, got %v", err)
	}
}
