package microbench_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	mockprov "github.com/Guilherme-Ciano/evalvet/internal/provider/mock"
	"github.com/Guilherme-Ciano/evalvet/internal/runner"
	"github.com/Guilherme-Ciano/evalvet/internal/tmpl"
)

const promptText = "Extract the vendor name, vendor VAT id, total, currency and date " +
	"from the invoice below. Reply with JSON only.\n\n{{ .document }}\n"

type nopStore struct{}

func (nopStore) Get(context.Context, cache.Key) (domain.Response, bool, error) {
	return domain.Response{}, false, nil
}

func (nopStore) Put(context.Context, cache.Key, domain.Response) error { return nil }

func caseData(i int) map[string]any {
	vendor := fmt.Sprintf("Vendor %05d Lda", i)
	total := float64(1000+i*37%9000) + float64(i%100)/100
	vat := fmt.Sprintf("PT%09d", 100000000+i)
	doc := fmt.Sprintf(
		"INVOICE PT-%05d\nVendor: %s\nVAT ID: %s\nDate: 2026-06-05\nItems: %d\nTotal: %.2f\nCurrency: EUR",
		i, vendor, vat, 1+i%9, total,
	)
	return map[string]any{
		"document": doc,
		"expected": map[string]any{"total": total, "vat_id": vat, "vendor": vendor},
		"case_id":  "inv#" + strconv.Itoa(i),
	}
}

func assertSpecs(data map[string]any) []config.AssertSpec {
	exp, ok := data["expected"].(map[string]any)
	if !ok {
		panic("expected map")
	}
	return []config.AssertSpec{
		{Kind: "json_valid"},
		{Kind: "json_path", Path: "$.total", Equals: strconv.FormatFloat(exp["total"].(float64), 'f', -1, 64)},
		{Kind: "contains", Value: exp["vendor"]},
	}
}

func compileAll(tb testing.TB, specs []config.AssertSpec) []assert.Assertion {
	tb.Helper()
	out := make([]assert.Assertion, 0, len(specs))
	for _, s := range specs {
		a, err := assert.Compile(s, assert.Deps{})
		if err != nil {
			tb.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func target() runner.Target {
	p := cache.Wrap(mockprov.New("p", 0), nopStore{}, cache.ModeRW, "in-process", &cache.Counters{})
	return runner.Target{
		Name: "head", Provider: p, ProviderKey: "mock|p", Model: "mock-good",
		Params: domain.Params{MaxTokens: 256},
	}
}

func BenchmarkTrial(b *testing.B) {
	data := caseData(42)
	specs := assertSpecs(data)
	t := target()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		msg, err := tmpl.Render("inv.prompt", promptText, data)
		if err != nil {
			b.Fatal(err)
		}
		c := runner.Case{ID: "inv#42", Messages: []string{msg}, Samples: 1, Asserts: compileAll(b, specs)}
		plan := runner.Plan{
			Targets:     []runner.Target{t},
			Cases:       []runner.Case{c},
			Concurrency: map[string]int{"mock|p": 1},
		}
		trials, err := runner.Run(ctx, plan)
		if err != nil {
			b.Fatal(err)
		}
		st := runner.Aggregate(t, plan.Cases, trials)
		if st.Trials != 1 || st.Errors != 0 {
			b.Fatalf("stats %+v", st)
		}
	}
}

func BenchmarkTrialBatch1000(b *testing.B) {
	const n = 1000
	t := target()
	cases := make([]runner.Case, n)
	for i := range cases {
		data := caseData(i)
		msg, err := tmpl.Render("inv.prompt", promptText, data)
		if err != nil {
			b.Fatal(err)
		}
		cases[i] = runner.Case{
			ID: "inv#" + strconv.Itoa(
				i,
			),
			Messages: []string{msg},
			Samples:  1,
			Asserts:  compileAll(b, assertSpecs(data)),
		}
	}
	plan := runner.Plan{Targets: []runner.Target{t}, Cases: cases, Concurrency: map[string]int{"mock|p": 768}}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		trials, err := runner.Run(ctx, plan)
		if err != nil {
			b.Fatal(err)
		}
		runner.Aggregate(t, plan.Cases, trials)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/trial")
}

func request() domain.Request {
	msg, err := tmpl.Render("inv.prompt", promptText, caseData(42))
	if err != nil {
		panic(err)
	}
	return domain.Request{
		Model:    "mock-good",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: msg}},
		Params:   domain.Params{MaxTokens: 256},
		Sample:   0,
		TrialID:  mockllm.TrialID("inv#42", 0),
	}
}

func BenchmarkComputeKey(b *testing.B) {
	req := request()
	raw, err := json.Marshal(req)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cache.RequestKey("mock", "in-process", req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCanonicalJSON(b *testing.B) {
	raw, err := json.Marshal(cache.KeyMaterial{
		Schema: cache.SchemaVersion, Provider: "mock", Endpoint: "in-process", Model: "mock-good", Request: request(),
	})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := cache.CanonicalJSON(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCacheHitFS(b *testing.B) {
	store := cache.NewFS(b.TempDir())
	ctx := context.Background()
	key, err := cache.RequestKey("mock", "in-process", request())
	if err != nil {
		b.Fatal(err)
	}
	resp := mockprov.New("p", 0)
	r, err := resp.Complete(ctx, request())
	if err != nil {
		b.Fatal(err)
	}
	if err := store.Put(ctx, key, r); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, ok, err := store.Get(ctx, key); err != nil || !ok {
			b.Fatal(ok, err)
		}
	}
}

func BenchmarkMockComplete(b *testing.B) {
	p := mockprov.New("p", 0)
	req := request()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := p.Complete(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAssert3(b *testing.B) {
	data := caseData(42)
	as := compileAll(b, assertSpecs(data))
	resp, err := mockprov.New("p", 0).Complete(context.Background(), request())
	if err != nil {
		b.Fatal(err)
	}
	in := assert.Input{CaseID: "inv#42", Output: resp.Text, Final: resp}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		for _, a := range as {
			if _, err := a.Evaluate(ctx, in); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRenderPrompt(b *testing.B) {
	data := caseData(42)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tmpl.Render("inv.prompt", promptText, data); err != nil {
			b.Fatal(err)
		}
	}
}
