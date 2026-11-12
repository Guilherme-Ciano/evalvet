package app

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/runner"
)

type progress struct {
	w      io.Writer
	mu     sync.Mutex
	total  int
	count  int
	cached int
	failed int
	start  time.Time
	last   time.Time
}

func newProgress(w io.Writer, targets int, cases []runner.Case) *progress {
	total := 0
	for _, c := range cases {
		total += c.Samples * targets
	}
	return &progress{w: w, total: total, start: time.Now()}
}

func (p *progress) add(t domain.Trial) {
	if p.w == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.count++
	if t.Cached {
		p.cached++
	}
	if t.Errored {
		p.failed++
	}
	if now := time.Now(); now.Sub(p.last) > 200*time.Millisecond || p.count == p.total {
		p.last = now
		rate := float64(p.count) / max(now.Sub(p.start).Seconds(), 1e-9)
		fmt.Fprintf(p.w, "\r%d/%d trials · %.0f/s · %d cached · %d errors", p.count, p.total, rate, p.cached, p.failed)
	}
}

func (p *progress) done() {
	if p.w == nil || p.count == 0 {
		return
	}
	fmt.Fprintln(p.w)
}
