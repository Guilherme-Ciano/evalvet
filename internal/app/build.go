package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/pricing"
	"github.com/Guilherme-Ciano/evalvet/internal/provider/anthropic"
	"github.com/Guilherme-Ciano/evalvet/internal/provider/mock"
	"github.com/Guilherme-Ciano/evalvet/internal/provider/openai"
	"github.com/Guilherme-Ciano/evalvet/internal/scheduler"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

type providerSet struct {
	providers   map[string]domain.Provider
	keys        map[string]string
	concurrency map[string]int
	gates       map[string]*scheduler.Gate
	counters    *cache.Counters
}

func providerKey(name string, p config.Provider, apiKey string) string {
	if p.Kind == config.KindMock {
		return "mock|" + name
	}
	sum := sha256.Sum256([]byte(apiKey))
	return p.Kind + "|" + p.BaseURL + "|" + hex.EncodeToString(sum[:4])
}

func defaultConcurrency(kind string) int {
	if kind == config.KindMock {
		return 64 * runtime.GOMAXPROCS(0)
	}
	return 8
}

func newRaw(name string, p config.Provider, apiKey string, client *http.Client, mockLatency time.Duration) domain.Provider {
	switch p.Kind {
	case config.KindAnthropic:
		return anthropic.New(anthropic.Config{Name: name, BaseURL: p.BaseURL, APIKey: apiKey, HTTPClient: client})
	case config.KindOpenAI, config.KindOpenAICompatible:
		return openai.New(openai.Config{Name: name, Compatible: p.Kind == config.KindOpenAICompatible, BaseURL: p.BaseURL, APIKey: apiKey, HTTPClient: client})
	default:
		return mock.New(name, mockLatency)
	}
}

func (a *App) apiKey(name string, p config.Provider) (string, error) {
	if p.Kind == config.KindMock {
		return "", nil
	}
	if p.APIKeyEnv != "" {
		if v, ok := a.opts.Env(p.APIKeyEnv); ok && v != "" {
			return v, nil
		}
	}
	if p.BaseURL != "" {
		return "unused", nil
	}
	return "", fmt.Errorf("provider %s: environment variable %s is not set: %w", name, p.APIKeyEnv, domain.ErrAuth)
}

func (a *App) priceEntry(suite *config.Suite, target string) (pricing.Entry, bool) {
	t := suite.Targets[target]
	p := suite.Providers[t.Provider]
	return a.pricing.Lookup([]string{t.Provider, p.Kind}, t.Model, a.opts.Clock())
}

func (a *App) costFunc(suite *config.Suite, target string) func(domain.Usage) *float64 {
	e, ok := a.priceEntry(suite, target)
	if !ok {
		return func(domain.Usage) *float64 { return nil }
	}
	return func(u domain.Usage) *float64 {
		if !u.Known {
			return nil
		}
		c := e.Cost(u)
		return &c
	}
}

func estimateTokens(req domain.Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Content)
		for _, r := range m.ToolResults {
			n += len(r.Content)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Parameters) + len(t.Description)
	}
	return n/4 + 1
}

func (a *App) buildProviders(suite *config.Suite, budget *scheduler.Budget) (*providerSet, error) {
	ps := &providerSet{
		providers:   map[string]domain.Provider{},
		keys:        map[string]string{},
		concurrency: map[string]int{},
		gates:       map[string]*scheduler.Gate{},
		counters:    &cache.Counters{},
	}
	store := cache.NewFS(a.opts.CacheDir)
	names := make([]string, 0, len(suite.Providers))
	for n := range suite.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		p := suite.Providers[name]
		key, err := a.apiKey(name, p)
		if err != nil {
			if a.opts.CacheMode == cache.ModeReplay {
				key = "replay"
			} else if a.usesProvider(suite, name) {
				return nil, err
			}
		}
		gk := providerKey(name, p, key)
		conc := p.Limits.Concurrency
		if conc <= 0 {
			conc = defaultConcurrency(p.Kind)
		}
		gate, ok := ps.gates[gk]
		if !ok {
			gate = scheduler.NewGate(scheduler.Limits{RPM: p.Limits.RPM, TPM: p.Limits.TPM, Concurrency: conc})
			ps.gates[gk] = gate
		}
		ps.keys[name] = gk
		ps.concurrency[gk] = max(ps.concurrency[gk], conc)
		client := transport.NewClient(transport.Options{RecordDir: a.opts.HTTPRecordDir, ReplayDir: a.opts.HTTPReplayDir})
		raw := newRaw(name, p, key, client, a.opts.MockLatency)
		gated := scheduler.Wrap(raw, gate, scheduler.Options{Budget: budget, MaxRetries: a.opts.MaxRetries, Estimate: a.estimator(suite, name), Price: a.pricer(suite, name)})
		endpoint := p.BaseURL
		if p.Kind == config.KindMock {
			endpoint = "in-process"
		}
		ps.providers[name] = cache.Wrap(gated, store, a.opts.CacheMode, endpoint, ps.counters)
	}
	return ps, nil
}

func (a *App) usesProvider(suite *config.Suite, provider string) bool {
	for _, t := range suite.Matrix.Targets {
		if suite.Targets[t].Provider == provider {
			return true
		}
	}
	if j := suite.Defaults.Judge.Target; j != "" && suite.Targets[j].Provider == provider {
		for _, c := range suite.Cases {
			for _, as := range c.Asserts {
				if as.Kind == "judge" {
					return true
				}
			}
		}
	}
	return false
}

func (a *App) targetsOfProvider(suite *config.Suite, provider string) []string {
	var out []string
	for name, t := range suite.Targets {
		if t.Provider == provider {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) estimator(suite *config.Suite, provider string) scheduler.Estimator {
	targets := a.targetsOfProvider(suite, provider)
	return func(req domain.Request) (int, float64, bool) {
		in := estimateTokens(req)
		out := int(min(req.Params.MaxTokens, 512))
		for _, tn := range targets {
			if suite.Targets[tn].Model != req.Model {
				continue
			}
			if e, ok := a.priceEntry(suite, tn); ok {
				return in + out, e.Cost(domain.Usage{InputTokens: int64(in), OutputTokens: int64(out)}), true
			}
		}
		return in + out, 0, false
	}
}

func (a *App) pricer(suite *config.Suite, provider string) scheduler.Pricer {
	targets := a.targetsOfProvider(suite, provider)
	return func(u domain.Usage) (float64, bool) {
		for _, tn := range targets {
			if e, ok := a.priceEntry(suite, tn); ok {
				return e.Cost(u), true
			}
		}
		return 0, false
	}
}

func modelFamily(model string) string {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "mock") {
		return ""
	}
	for _, f := range []string{"claude", "gpt", "gemini", "llama", "qwen", "mistral"} {
		if strings.Contains(m, f) {
			return f
		}
	}
	return m
}

func toParams(t config.Target) domain.Params {
	return domain.Params{
		MaxTokens:       t.Params.MaxTokens,
		Seed:            t.Params.Seed,
		Temperature:     t.Params.Temperature,
		Effort:          t.Params.Anthropic.Effort,
		ReasoningEffort: t.Params.OpenAI.ReasoningEffort,
	}
}
