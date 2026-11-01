package haclient

import (
	"context"
	"testing"
	"time"
)

func TestRestoreSessionRunnerAllowsOneSessionPerInstance(t *testing.T) {
	runner := NewRestoreSessionRunner(time.Second)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := runner.StartSession(context.Background(), "instance", func(context.Context) { close(started); <-release }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := runner.StartSession(context.Background(), "instance", func(context.Context) {}); err != ErrRestoreSessionExists {
		t.Fatalf("got %v", err)
	}
	close(release)
	for runner.Active("instance") {
		time.Sleep(time.Millisecond)
	}
}

func TestRestoreSessionStopsWithManagerContext(t *testing.T) {
	runner := NewRestoreSessionRunner(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	if err := runner.StartSession(ctx, "instance", func(ctx context.Context) { <-ctx.Done(); close(done) }); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop")
	}
}

func TestRestoreSessionRunnerEnforcesGlobalCapacity(t *testing.T) {
	runner := NewRestoreSessionRunner(time.Minute)
	release := make(chan struct{})
	started := make(chan struct{}, MaxRestoreSessions)
	for i := 0; i < MaxRestoreSessions; i++ {
		instance := string(rune('a' + i))
		if err := runner.StartSession(context.Background(), instance, func(context.Context) { started <- struct{}{}; <-release }); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	for i := 0; i < MaxRestoreSessions; i++ {
		<-started
	}
	if err := runner.StartSession(context.Background(), "overflow", func(context.Context) {}); err != ErrRestoreSessionLimit {
		t.Fatalf("overflow error = %v, want capacity error", err)
	}
	close(release)
}

func TestRestoreSessionRunnerExpiresSession(t *testing.T) {
	runner := NewRestoreSessionRunner(5 * time.Millisecond)
	done := make(chan struct{})
	if err := runner.StartSession(context.Background(), "instance", func(ctx context.Context) { <-ctx.Done(); close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not expire")
	}
}

func TestNewRunnerDoesNotRecoverInMemorySessionAuthority(t *testing.T) {
	original := NewRestoreSessionRunner(time.Minute)
	block := make(chan struct{})
	if err := original.StartSession(context.Background(), "instance", func(context.Context) { <-block }); err != nil {
		t.Fatal(err)
	}
	if !original.Active("instance") {
		t.Fatal("original runner has no active session")
	}
	replacement := NewRestoreSessionRunner(time.Minute)
	if replacement.Active("instance") {
		t.Fatal("replacement recovered in-memory session state")
	}
	close(block)
}
