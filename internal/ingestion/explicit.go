package ingestion

import (
	"github.com/varve-sh/varve/internal/types"
)

// Validate checks a MemorySaveInput for correctness before saving.
func Validate(input *types.MemorySaveInput) error {
	if input.Content == "" {
		return &types.ValidationError{Field: "content", Message: "must not be empty"}
	}
	if len(input.Content) > 10000 {
		return &types.ValidationError{Field: "content", Message: "must not exceed 10000 characters"}
	}
	if input.Type != "" {
		switch input.Type {
		case types.MemoryTypeDecision, types.MemoryTypeConvention,
			types.MemoryTypeFact, types.MemoryTypeEvent:
			// valid
		default:
			return &types.ValidationError{Field: "type", Message: "must be decision, convention, fact, or event"}
		}
	}
	if input.Confidence != 0 && (input.Confidence < 0.0 || input.Confidence > 1.0) {
		return &types.ValidationError{Field: "confidence", Message: "must be between 0.0 and 1.0"}
	}
	if len(input.Tags) > 20 {
		return &types.ValidationError{Field: "tags", Message: "must not exceed 20 tags"}
	}
	for _, tag := range input.Tags {
		if tag == "" {
			return &types.ValidationError{Field: "tags", Message: "tags must not be empty strings"}
		}
		if len(tag) > 50 {
			return &types.ValidationError{Field: "tags", Message: "each tag must not exceed 50 characters"}
		}
	}
	if len(input.FilePaths) > 20 {
		return &types.ValidationError{Field: "file_paths", Message: "must not exceed 20 file paths"}
	}
	for _, p := range input.FilePaths {
		if p == "" {
			return &types.ValidationError{Field: "file_paths", Message: "file paths must not be empty strings"}
		}
		if len(p) > 0 && p[0] == '/' {
			return &types.ValidationError{Field: "file_paths", Message: "file paths must be relative (no leading /)"}
		}
	}
	return nil
}
