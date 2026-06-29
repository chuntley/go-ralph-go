package runner

import (
	"context"

	"github.com/chuntley/go-ralph-go/internal/claude"
)

// agentSession is the slice of *claude.Session the runner actually depends on.
// Depending on an interface (rather than the concrete type) lets tests inject a
// fake agent and exercise the full orchestration — the parallel dispatcher, the
// merge-repair loop, PR/merge — without a real `claude` binary. *claude.Session
// satisfies it.
type agentSession interface {
	Reset() error
	StartFresh() error
	Run(ctx context.Context, prompt string) error
	LastTurnText() string
	WriteBanner(text string) error
	Close() error
}

// sessionFactory builds an agentSession for a workspace. Defaults to wrapping
// claude.NewSession; tests override run.newSession to inject a fake.
type sessionFactory func(claude.SessionConfig) (agentSession, error)

// makeSession builds a session via the injected factory, falling back to the
// real claude.NewSession when none is set (the production path).
func (r *run) makeSession(cfg claude.SessionConfig) (agentSession, error) {
	if r.newSession != nil {
		return r.newSession(cfg)
	}
	return claude.NewSession(cfg)
}
