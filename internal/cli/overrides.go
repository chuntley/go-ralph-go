package cli

import (
	"fmt"
	"time"

	"github.com/chuntley/go-ralph-go/internal/config"
)

// applyOverrides folds CLI-level overrides onto a loaded Config. Zero/empty
// values mean "use what's already in cfg" — except passRetries, where 0 is a
// meaningful value (fail on first crash), so its "unset" sentinel is any
// negative number. Returns an error if the result is outside the validation
// bounds (e.g. --iterations too high, or a min/max that crosses over).
func applyOverrides(cfg *config.Config, minIterations, iterations int, instructionsDoc string, passTimeout time.Duration, passRetries int) error {
	if minIterations > 0 {
		cfg.MinIterations = minIterations
	}
	if iterations > 0 {
		cfg.Iterations = iterations
	}
	if instructionsDoc != "" {
		cfg.InstructionsDoc = instructionsDoc
	}
	if passTimeout > 0 {
		cfg.PassTimeout = passTimeout.String()
	}
	if passRetries >= 0 {
		cfg.PassRetries = passRetries
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid override: %w", err)
	}
	return nil
}
