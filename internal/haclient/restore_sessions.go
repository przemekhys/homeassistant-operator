package haclient

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	MaxRestoreSessions     = 4
	DefaultRestoreLifetime = 15 * time.Minute
)

var (
	ErrRestoreSessionExists = errors.New("a restore session already exists for this instance")
	ErrRestoreSessionLimit  = errors.New("restore session capacity reached")
)

// RestoreSessionRunner is a manager Runnable. It only owns transport lifetime;
// durable coordination must authorize dispatch before Start is called. Losing a
// runner never manufactures a replacement dispatch authority.
type RestoreSessionRunner struct {
	mu       sync.Mutex
	lifetime time.Duration
	sessions map[string]context.CancelFunc
	sem      chan struct{}
}

func NewRestoreSessionRunner(lifetime time.Duration) *RestoreSessionRunner {
	if lifetime <= 0 {
		lifetime = DefaultRestoreLifetime
	}
	return &RestoreSessionRunner{
		lifetime: lifetime,
		sessions: make(map[string]context.CancelFunc),
		sem:      make(chan struct{}, MaxRestoreSessions),
	}
}

func (r *RestoreSessionRunner) NeedLeaderElection() bool { return true }

// Start waits for cancellation; active child sessions receive the manager
// context and stop with it. It does not restart or redispatch them.
func (r *RestoreSessionRunner) Start(ctx context.Context) error {
	<-ctx.Done()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.sessions {
		cancel()
	}
	return nil
}

// StartSession starts exactly one bounded transport observation for instance.
// fn is invoked once and must not retry a native command internally.
func (r *RestoreSessionRunner) StartSession(ctx context.Context, instanceID string, fn func(context.Context)) error {
	r.mu.Lock()
	if _, exists := r.sessions[instanceID]; exists {
		r.mu.Unlock()
		return ErrRestoreSessionExists
	}
	select {
	case r.sem <- struct{}{}:
	default:
		r.mu.Unlock()
		return ErrRestoreSessionLimit
	}
	sessionCtx, cancel := context.WithTimeout(ctx, r.lifetime)
	r.sessions[instanceID] = cancel
	r.mu.Unlock()
	go func() {
		defer func() { <-r.sem; r.mu.Lock(); delete(r.sessions, instanceID); r.mu.Unlock() }()
		fn(sessionCtx)
	}()
	return nil
}

func (r *RestoreSessionRunner) Active(instanceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[instanceID]
	return ok
}
