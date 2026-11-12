package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type Limits struct {
	RPM         int
	TPM         int
	Concurrency int
}

type Gate struct {
	sem *semaphore.Weighted
	rpm *rate.Limiter
	tpm *rate.Limiter

	mu    sync.Mutex
	until time.Time

	throttled int64
}

func NewGate(l Limits) *Gate {
	g := &Gate{}
	if l.Concurrency > 0 {
		g.sem = semaphore.NewWeighted(int64(l.Concurrency))
	}
	if l.RPM > 0 {
		g.rpm = rate.NewLimiter(rate.Limit(float64(l.RPM)/60), max(1, l.RPM/60))
	}
	if l.TPM > 0 {
		g.tpm = rate.NewLimiter(rate.Limit(float64(l.TPM)/60), l.TPM)
	}
	return g
}

func (g *Gate) Pause(d time.Duration) {
	if d <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.throttled++
	if t := time.Now().Add(d); t.After(g.until) {
		g.until = t
	}
}

func (g *Gate) Throttled() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.throttled
}

func (g *Gate) waitPause(ctx context.Context) error {
	for {
		g.mu.Lock()
		d := time.Until(g.until)
		g.mu.Unlock()
		if d <= 0 {
			return nil
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("wait provider pause: %w", context.Cause(ctx))
		case <-t.C:
		}
	}
}

func (g *Gate) Acquire(ctx context.Context, tokens int) (func(), error) {
	if err := g.waitPause(ctx); err != nil {
		return nil, err
	}
	if g.sem != nil {
		if err := g.sem.Acquire(ctx, 1); err != nil {
			return nil, fmt.Errorf("acquire provider slot: %w", context.Cause(ctx))
		}
	}
	release := func() {
		if g.sem != nil {
			g.sem.Release(1)
		}
	}
	if g.rpm != nil {
		if err := g.rpm.Wait(ctx); err != nil {
			release()
			return nil, fmt.Errorf("rpm limiter: %w", err)
		}
	}
	if g.tpm != nil && tokens > 0 {
		n := min(tokens, g.tpm.Burst())
		if err := g.tpm.WaitN(ctx, n); err != nil {
			release()
			return nil, fmt.Errorf("tpm limiter: %w", err)
		}
	}
	if err := g.waitPause(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (g *Gate) Charge(extraTokens int) {
	if g.tpm == nil || extraTokens <= 0 {
		return
	}
	g.tpm.ReserveN(time.Now(), min(extraTokens, g.tpm.Burst()))
}

type Budget struct {
	mu       sync.Mutex
	limit    float64
	spent    float64
	reserved float64
}

func NewBudget(limitUSD float64) *Budget {
	if limitUSD <= 0 {
		return nil
	}
	return &Budget{limit: limitUSD}
}

func (b *Budget) Reserve(est float64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent+b.reserved+est > b.limit {
		return fmt.Errorf(
			"spent $%.4f + reserved $%.4f + next $%.4f > $%.2f: %w",
			b.spent,
			b.reserved,
			est,
			b.limit,
			domain.ErrBudgetExceeded,
		)
	}
	b.reserved += est
	return nil
}

func (b *Budget) Commit(est, actual float64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reserved = math.Max(0, b.reserved-est)
	b.spent += actual
}

func (b *Budget) Spent() float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

type Estimator func(req domain.Request) (tokens int, costUSD float64, priced bool)

type Pricer func(u domain.Usage) (float64, bool)

type Gated struct {
	inner      domain.Provider
	gate       *Gate
	budget     *Budget
	estimate   Estimator
	price      Pricer
	maxRetries int
	backoff    time.Duration
}

type Options struct {
	Budget     *Budget
	Estimate   Estimator
	Price      Pricer
	MaxRetries int
	Backoff    time.Duration
}

func Wrap(inner domain.Provider, gate *Gate, o Options) *Gated {
	if o.Backoff <= 0 {
		o.Backoff = 500 * time.Millisecond
	}
	return &Gated{
		inner:      inner,
		gate:       gate,
		budget:     o.Budget,
		estimate:   o.Estimate,
		price:      o.Price,
		maxRetries: o.MaxRetries,
		backoff:    o.Backoff,
	}
}

func (g *Gated) Name() string { return g.inner.Name() }
func (g *Gated) Kind() string { return g.inner.Kind() }

func (g *Gated) Capabilities() domain.Capabilities {
	return domain.CapabilitiesOf(g.inner)
}

func (g *Gated) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	tokens, est := 0, 0.0
	if g.estimate != nil {
		tokens, est, _ = g.estimate(req)
	}
	var lastErr error
	for attempt := 0; attempt <= g.maxRetries; attempt++ {
		resp, err := g.once(ctx, req, tokens, est)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var pe *domain.ProviderError
		if !errors.As(err, &pe) || !pe.Retryable || errors.Is(err, domain.ErrAuth) {
			return resp, err
		}
		wait := pe.RetryAfter
		if wait <= 0 {
			wait = g.backoff * time.Duration(1<<min(attempt, 6))
			wait += time.Duration(rand.Int64N(int64(wait)/2 + 1))
		}
		g.gate.Pause(wait)
	}
	return domain.Response{}, fmt.Errorf("after %d retries: %w", g.maxRetries, lastErr)
}

func (g *Gated) once(ctx context.Context, req domain.Request, tokens int, est float64) (domain.Response, error) {
	if err := g.budget.Reserve(est); err != nil {
		return domain.Response{}, err
	}
	release, err := g.gate.Acquire(ctx, tokens)
	if err != nil {
		g.budget.Commit(est, 0)
		return domain.Response{}, err
	}
	resp, err := g.inner.Complete(ctx, req)
	release()
	actual := 0.0
	if err == nil && g.price != nil {
		if c, ok := g.price(resp.Usage); ok {
			actual = c
		}
		used := int(resp.Usage.InputTokens + resp.Usage.CachedInputTokens + resp.Usage.OutputTokens)
		g.gate.Charge(used - tokens)
	}
	g.budget.Commit(est, actual)
	return resp, err
}
