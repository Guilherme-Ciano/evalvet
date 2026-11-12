package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

type Provider struct {
	name    string
	engine  *mockllm.Engine
	latency time.Duration
}

func New(name string, latency time.Duration) *Provider {
	return &Provider{name: name, engine: mockllm.New(), latency: latency}
}

func (p *Provider) Name() string { return p.name }
func (p *Provider) Kind() string { return "mock" }

func (p *Provider) Capabilities() domain.Capabilities {
	return domain.Capabilities{Temperature: true, Seed: true, Tools: true}
}

func (p *Provider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	start := time.Now()
	if err := ctx.Err(); err != nil {
		return domain.Response{}, fmt.Errorf("mock %s: %w", p.name, err)
	}
	if p.latency > 0 {
		t := time.NewTimer(p.latency)
		select {
		case <-ctx.Done():
			t.Stop()
			return domain.Response{}, fmt.Errorf("mock %s: %w", p.name, ctx.Err())
		case <-t.C:
		}
	}
	resp := p.engine.Respond(req, req.TrialID)
	total := time.Since(start)
	resp.Timing = domain.Timing{Start: start, TTFB: total, FirstToken: total, Total: total}
	return resp, nil
}
