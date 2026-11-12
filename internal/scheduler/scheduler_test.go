package scheduler_test

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/scheduler"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeProvider struct {
	calls    atomic.Int64
	inflight atomic.Int64
	maxSeen  atomic.Int64
	script   func(n int) (domain.Response, error)
	work     func()
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Kind() string { return "fake-kind" }

func (f *fakeProvider) Complete(context.Context, domain.Request) (domain.Response, error) {
	n := int(f.calls.Add(1))
	cur := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if cur <= m || f.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	if f.work != nil {
		f.work()
	}
	if f.script != nil {
		return f.script(n)
	}
	return domain.Response{Text: "ok", Usage: domain.Usage{InputTokens: 10, OutputTokens: 10, Known: true}}, nil
}

func retryable(after time.Duration) error {
	return &domain.ProviderError{
		Provider:   "fake",
		StatusCode: http.StatusTooManyRequests,
		Message:    "slow down",
		Retryable:  true,
		RetryAfter: after,
	}
}

func TestGateConcurrencyEnforcedByAcquire(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{Concurrency: 3})
	releases := make([]func(), 0, 3)
	for range 3 {
		rel, err := g.Acquire(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, rel)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Acquire(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("4th acquire err = %v, want context.Canceled", err)
	}
	releases[0]()
	rel, err := g.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	rel()
	releases[1]()
	releases[2]()
}

func TestGateConcurrencyLimitUnderLoad(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{1, 2, 5} {
		g := scheduler.NewGate(scheduler.Limits{Concurrency: limit})
		fp := &fakeProvider{work: func() {
			for range 20 {
				runtime.Gosched()
			}
		}}
		p := scheduler.Wrap(fp, g, scheduler.Options{})
		var wg sync.WaitGroup
		for range 60 {
			wg.Go(func() {
				if _, err := p.Complete(context.Background(), domain.Request{}); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if got := fp.maxSeen.Load(); got > int64(limit) || got < 1 {
			t.Fatalf("limit %d: max in flight = %d", limit, got)
		}
		if fp.calls.Load() != 60 {
			t.Fatalf("calls = %d", fp.calls.Load())
		}
	}
}

func TestGateNoLimitsNeverBlocks(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{})
	for range 100 {
		rel, err := g.Acquire(context.Background(), 1_000_000)
		if err != nil {
			t.Fatal(err)
		}
		rel()
	}
	g.Charge(1_000_000)
	if g.Throttled() != 0 {
		t.Fatal("throttled must be zero")
	}
}

func TestGatePauseBlocksUntilDeadline(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{})
	const d = 60 * time.Millisecond
	start := time.Now()
	g.Pause(d)
	rel, err := g.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if el := time.Since(start); el < d {
		t.Fatalf("Acquire returned after %v, want >= %v", el, d)
	}
	if g.Throttled() != 1 {
		t.Fatalf("throttled = %d", g.Throttled())
	}
}

func TestGatePauseNonPositiveIgnored(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{})
	g.Pause(0)
	g.Pause(-time.Second)
	if g.Throttled() != 0 {
		t.Fatalf("throttled = %d", g.Throttled())
	}
	rel, err := g.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	rel()
}

func TestGatePauseNeverShortens(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{})
	g.Pause(time.Hour)
	g.Pause(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if g.Throttled() != 2 {
		t.Fatalf("throttled = %d", g.Throttled())
	}
}

func TestGatePauseRespectsContextCancel(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{Concurrency: 1})
	g.Pause(time.Hour)
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("stop now")
	done := make(chan error, 1)
	go func() {
		_, err := g.Acquire(ctx, 0)
		done <- err
	}()
	cancel(cause)
	if err := <-done; !errors.Is(err, cause) {
		t.Fatalf("err = %v, want cause", err)
	}
}

func TestGateAcquireExtendedPauseDuringWait(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{})
	g.Pause(30 * time.Millisecond)
	start := time.Now()
	done := make(chan struct{})
	go func() {
		rel, err := g.Acquire(context.Background(), 0)
		if err == nil {
			rel()
		}
		close(done)
	}()
	g.Pause(80 * time.Millisecond)
	<-done
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("elapsed %v, pause extension ignored", el)
	}
}

func TestGateRPMPacing(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{RPM: 600})
	start := time.Now()
	for range 12 {
		rel, err := g.Acquire(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		rel()
	}
	if el := time.Since(start); el < 150*time.Millisecond {
		t.Fatalf("12 acquires at 10/s burst 10 took %v, want >= 150ms", el)
	}
}

func TestGateRPMContextDeadline(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{RPM: 1, Concurrency: 1})
	rel, err := g.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx, 0); err == nil {
		t.Fatal("want rate limiter error")
	}
}

func TestGateTPMPacingAndCharge(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{TPM: 60000})
	start := time.Now()
	rel, err := g.Acquire(context.Background(), 60000)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel, err = g.Acquire(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("second acquire did not wait for tokens: %v", el)
	}

	g2 := scheduler.NewGate(scheduler.Limits{TPM: 60000})
	g2.Charge(0)
	g2.Charge(-5)
	g2.Charge(1_000_000)
	start = time.Now()
	rel, err = g2.Acquire(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	rel()
	if el := time.Since(start); el < 80*time.Millisecond {
		t.Fatalf("Charge did not drain the budget: %v", el)
	}
}

func TestGateTPMRequestLargerThanBurstDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	g := scheduler.NewGate(scheduler.Limits{TPM: 1000})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rel, err := g.Acquire(ctx, 10_000_000)
	if err != nil {
		t.Fatalf("oversized request must be clamped to burst: %v", err)
	}
	rel()
}

func TestBudgetNilIsUnlimited(t *testing.T) {
	t.Parallel()
	for _, limit := range []float64{0, -1} {
		b := scheduler.NewBudget(limit)
		if b != nil {
			t.Fatalf("NewBudget(%v) must be nil", limit)
		}
		if err := b.Reserve(1e9); err != nil {
			t.Fatal(err)
		}
		b.Commit(1e9, 1e9)
		if b.Spent() != 0 {
			t.Fatal("nil budget spent must be 0")
		}
	}
}

func TestBudgetReserveCommit(t *testing.T) {
	t.Parallel()
	b := scheduler.NewBudget(1.0)
	if err := b.Reserve(0.6); err != nil {
		t.Fatal(err)
	}
	if err := b.Reserve(0.5); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("err = %v", err)
	}
	b.Commit(0.6, 0.2)
	if b.Spent() != 0.2 {
		t.Fatalf("spent = %v", b.Spent())
	}
	if err := b.Reserve(0.8); err != nil {
		t.Fatalf("reserve after commit: %v", err)
	}
	if err := b.Reserve(0.01); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("err = %v", err)
	}
	b.Commit(0.8, 0)
	b.Commit(5, 0)
	if err := b.Reserve(0.8); err != nil {
		t.Fatalf("over-commit must not push reserved negative: %v", err)
	}
	if err := b.Reserve(0.01); !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatal("reserved must not go negative and then allow overspend")
	}
}

func TestBudgetExactLimitAllowed(t *testing.T) {
	t.Parallel()
	b := scheduler.NewBudget(1)
	if err := b.Reserve(1); err != nil {
		t.Fatalf("spending exactly the limit must pass: %v", err)
	}
}

func TestBudgetConcurrentReserveNeverOverspends(t *testing.T) {
	t.Parallel()
	const goroutines = 200
	b := scheduler.NewBudget(100)
	var ok, denied atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			if err := b.Reserve(1); err != nil {
				if !errors.Is(err, domain.ErrBudgetExceeded) {
					t.Error(err)
				}
				denied.Add(1)
				return
			}
			ok.Add(1)
			b.Commit(1, 1)
		})
	}
	wg.Wait()
	if ok.Load()+denied.Load() != goroutines {
		t.Fatalf("ok=%d denied=%d", ok.Load(), denied.Load())
	}
	if ok.Load() > 100 || b.Spent() > 100 {
		t.Fatalf("overspent: ok=%d spent=%v", ok.Load(), b.Spent())
	}
	if b.Spent() != float64(ok.Load()) {
		t.Fatalf("spent %v != successful reservations %d", b.Spent(), ok.Load())
	}
}

func TestBudgetConcurrentHeldReservationsCapAtLimit(t *testing.T) {
	t.Parallel()
	b := scheduler.NewBudget(50)
	var ok atomic.Int64
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if b.Reserve(1) == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 50 {
		t.Fatalf("reservations granted = %d, want exactly 50", ok.Load())
	}
}

func TestGatedSuccessPassThrough(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	g := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{})
	if g.Name() != "fake" || g.Kind() != "fake-kind" {
		t.Fatalf("name=%q kind=%q", g.Name(), g.Kind())
	}
	if caps := g.Capabilities(); !caps.Tools || caps.Seed {
		t.Fatalf("caps = %+v", caps)
	}
	resp, err := g.Complete(context.Background(), domain.Request{})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestGatedRetriesHonoringRetryAfter(t *testing.T) {
	t.Parallel()
	gate := scheduler.NewGate(scheduler.Limits{})
	fp := &fakeProvider{script: func(n int) (domain.Response, error) {
		if n <= 2 {
			return domain.Response{}, retryable(25 * time.Millisecond)
		}
		return domain.Response{Text: "finally"}, nil
	}}
	p := scheduler.Wrap(fp, gate, scheduler.Options{MaxRetries: 3, Backoff: time.Hour})
	start := time.Now()
	resp, err := p.Complete(context.Background(), domain.Request{})
	if err != nil || resp.Text != "finally" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	el := time.Since(start)
	if fp.calls.Load() != 3 {
		t.Fatalf("calls = %d", fp.calls.Load())
	}
	if el < 50*time.Millisecond {
		t.Fatalf("RetryAfter ignored: elapsed %v", el)
	}
	if el > 30*time.Second {
		t.Fatalf("RetryAfter must take priority over backoff: %v", el)
	}
	if gate.Throttled() != 2 {
		t.Fatalf("throttled = %d", gate.Throttled())
	}
}

func TestGatedExponentialBackoffWhenNoRetryAfter(t *testing.T) {
	t.Parallel()
	gate := scheduler.NewGate(scheduler.Limits{})
	fp := &fakeProvider{script: func(n int) (domain.Response, error) {
		if n <= 2 {
			return domain.Response{}, retryable(0)
		}
		return domain.Response{Text: "ok"}, nil
	}}
	p := scheduler.Wrap(fp, gate, scheduler.Options{MaxRetries: 5, Backoff: 10 * time.Millisecond})
	start := time.Now()
	if _, err := p.Complete(context.Background(), domain.Request{}); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 30*time.Millisecond {
		t.Fatalf("backoff 10ms + 20ms expected, elapsed %v", el)
	}
}

func TestGatedGivesUpAfterMaxRetries(t *testing.T) {
	t.Parallel()
	for _, maxRetries := range []int{0, 1, 3} {
		fp := &fakeProvider{
			script: func(int) (domain.Response, error) { return domain.Response{}, retryable(time.Millisecond) },
		}
		p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{MaxRetries: maxRetries})
		_, err := p.Complete(context.Background(), domain.Request{})
		if fp.calls.Load() != int64(maxRetries+1) {
			t.Fatalf("maxRetries %d: calls = %d", maxRetries, fp.calls.Load())
		}
		var pe *domain.ProviderError
		if !errors.As(err, &pe) || pe.StatusCode != http.StatusTooManyRequests || !errors.Is(err, domain.ErrProvider) {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestGatedDoesNotRetryNonRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"401 retryable flag set", &domain.ProviderError{Provider: "p", StatusCode: 401, Retryable: true}},
		{"403 retryable flag set", &domain.ProviderError{Provider: "p", StatusCode: 403, Retryable: true}},
		{"400 not retryable", &domain.ProviderError{Provider: "p", StatusCode: 400}},
		{"plain error", errors.New("boom")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fp := &fakeProvider{script: func(int) (domain.Response, error) { return domain.Response{}, tt.err }}
			p := scheduler.Wrap(
				fp,
				scheduler.NewGate(scheduler.Limits{}),
				scheduler.Options{MaxRetries: 5, Backoff: time.Millisecond},
			)
			_, err := p.Complete(context.Background(), domain.Request{})
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v", err)
			}
			if fp.calls.Load() != 1 {
				t.Fatalf("calls = %d, want 1", fp.calls.Load())
			}
		})
	}
}

func TestGatedAuthErrorIsAuth(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{script: func(int) (domain.Response, error) {
		return domain.Response{}, &domain.ProviderError{Provider: "p", StatusCode: 401, Retryable: true}
	}}
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{MaxRetries: 3})
	if _, err := p.Complete(context.Background(), domain.Request{}); !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}

func TestGatedBudgetExceededSkipsProvider(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	budget := scheduler.NewBudget(0.01)
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{
		Budget:     budget,
		Estimate:   func(domain.Request) (int, float64, bool) { return 10, 0.02, true },
		MaxRetries: 3,
	})
	_, err := p.Complete(context.Background(), domain.Request{})
	if !errors.Is(err, domain.ErrBudgetExceeded) {
		t.Fatalf("err = %v", err)
	}
	if fp.calls.Load() != 0 {
		t.Fatal("provider must not be called")
	}
}

func TestGatedBudgetAccounting(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	budget := scheduler.NewBudget(1)
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{
		Budget:   budget,
		Estimate: func(domain.Request) (int, float64, bool) { return 10, 0.4, true },
		Price:    func(domain.Usage) (float64, bool) { return 0.1, true },
	})
	for range 5 {
		if _, err := p.Complete(context.Background(), domain.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := budget.Spent(); got < 0.5-1e-9 || got > 0.5+1e-9 {
		t.Fatalf("spent = %v, want 0.5 (actual price, reservations released)", got)
	}
}

func TestGatedFailedCallReleasesReservation(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{script: func(int) (domain.Response, error) { return domain.Response{}, errors.New("boom") }}
	budget := scheduler.NewBudget(1)
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{
		Budget:   budget,
		Estimate: func(domain.Request) (int, float64, bool) { return 0, 0.9, true },
	})
	for range 5 {
		if _, err := p.Complete(
			context.Background(),
			domain.Request{},
		); err == nil ||
			errors.Is(err, domain.ErrBudgetExceeded) {
			t.Fatalf("err = %v", err)
		}
	}
	if budget.Spent() != 0 {
		t.Fatalf("spent = %v", budget.Spent())
	}
}

func TestGatedCancelledContextReleasesReservation(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	budget := scheduler.NewBudget(1)
	gate := scheduler.NewGate(scheduler.Limits{})
	gate.Pause(time.Hour)
	p := scheduler.Wrap(fp, gate, scheduler.Options{
		Budget:   budget,
		Estimate: func(domain.Request) (int, float64, bool) { return 0, 0.9, true },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 3 {
		if _, err := p.Complete(ctx, domain.Request{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	}
	if err := budget.Reserve(0.9); err != nil {
		t.Fatalf("reservation leaked: %v", err)
	}
	if fp.calls.Load() != 0 {
		t.Fatal("provider must not be called")
	}
}

func TestGatedConcurrentBudgetRace(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	budget := scheduler.NewBudget(1000)
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{Concurrency: 8}), scheduler.Options{
		Budget:   budget,
		Estimate: func(domain.Request) (int, float64, bool) { return 5, 1, true },
		Price:    func(domain.Usage) (float64, bool) { return 1, true },
	})
	var wg sync.WaitGroup
	var exceeded atomic.Int64
	for range 200 {
		wg.Go(func() {
			if _, err := p.Complete(context.Background(), domain.Request{}); errors.Is(err, domain.ErrBudgetExceeded) {
				exceeded.Add(1)
			}
		})
	}
	wg.Wait()
	if exceeded.Load() != 0 || budget.Spent() != 200 {
		t.Fatalf("exceeded=%d spent=%v", exceeded.Load(), budget.Spent())
	}
}

func TestGatedUnpricedResponseSpendsNothing(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{}
	budget := scheduler.NewBudget(1)
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{TPM: 600000}), scheduler.Options{
		Budget: budget,
		Price:  func(domain.Usage) (float64, bool) { return 0, false },
	})
	if _, err := p.Complete(context.Background(), domain.Request{}); err != nil {
		t.Fatal(err)
	}
	if budget.Spent() != 0 {
		t.Fatalf("spent = %v", budget.Spent())
	}
}

func TestGatedContextCancelledDuringBackoffStops(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{script: func(int) (domain.Response, error) { return domain.Response{}, retryable(time.Hour) }}
	p := scheduler.Wrap(fp, scheduler.NewGate(scheduler.Limits{}), scheduler.Options{MaxRetries: 5})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := p.Complete(ctx, domain.Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if fp.calls.Load() != 1 {
		t.Fatalf("calls = %d", fp.calls.Load())
	}
}
