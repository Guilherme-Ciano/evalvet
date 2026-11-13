package scheduler_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/scheduler"
)

type blockingProvider struct {
	calls atomic.Int64
}

func (b *blockingProvider) Name() string { return "slow" }
func (b *blockingProvider) Kind() string { return "slow-kind" }

func (b *blockingProvider) Complete(ctx context.Context, _ domain.Request) (domain.Response, error) {
	b.calls.Add(1)
	<-ctx.Done()
	return domain.Response{}, ctx.Err()
}

func TestAttemptTimeoutIsRetryableAndPerAttempt(t *testing.T) {
	t.Parallel()
	bp := &blockingProvider{}
	g := scheduler.Wrap(bp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{
		MaxRetries: 2,
		Backoff:    time.Millisecond,
		Timeout:    20 * time.Millisecond,
	})
	_, err := g.Complete(context.Background(), domain.Request{Model: "m"})
	pe, ok := errors.AsType[*domain.ProviderError](err)
	if !ok || !pe.Retryable || pe.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("err = %v", err)
	}
	if got := bp.calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestCallerCancellationIsNotATimeout(t *testing.T) {
	t.Parallel()
	bp := &blockingProvider{}
	g := scheduler.Wrap(bp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{Timeout: time.Hour})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := g.Complete(ctx, domain.Request{Model: "m"})
	if _, ok := errors.AsType[*domain.ProviderError](err); ok {
		t.Fatalf("caller cancellation must not become a provider error: %v", err)
	}
}
