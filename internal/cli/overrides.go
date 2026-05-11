package cli

import (
	"fmt"

	"github.com/chuntley/go-ralph-go/internal/config"
)

// applyOverrides folds CLI-level overrides onto a loaded Config. Zero/empty
// values mean "use what's already in cfg". Returns an error if the result is
// outside the validation bounds (e.g. --iterations too high).
func applyOverrides(cfg *config.Config, iterations int, instructionsDoc string) error {
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
