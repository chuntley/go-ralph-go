package cli

import (
	"fmt"

	"github.com/chuntley/go-ralph-go/internal/config"
)

// applyOverrides folds CLI-level overrides onto a loaded Config. Zero/empty
// values mean "use what's already in cfg". Returns an error if the result is
// outside the validation bounds (e.g. --iterations too high, or a min/max that
// crosses over).
func applyOverrides(cfg *config.Config, minIterations, iterations int, instructionsDoc string) error {
	if minIterations > 0 {
		cfg.MinIterations = minIterations
	}
	if iterations > 0 {
		cfg.Iterations = iterations
	}
	if instructionsDoc != "" {
		cfg.InstructionsDoc = instructionsDoc
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid override: %w", err)
	}
	return nil
}
