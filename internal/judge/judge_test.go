package judge_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/judge"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	mockprov "github.com/Guilherme-Ciano/evalvet/internal/provider/mock"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type nopProvider struct{ name string }

func (p nopProvider) Name() string { return p.name }
func (p nopProvider) Kind() string { return "nop" }
func (p nopProvider) Complete(context.Context, domain.Request) (domain.Response, error) {
	return domain.Response{}, errors.New("unused")
}

type fakeCaller struct {
	mu    sync.Mutex
	texts []string
	err   error
	reqs  []domain.Request
	provs []domain.Provider
}

func (f *fakeCaller) call(_ context.Context, p domain.Provider, req domain.Request) (domain.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.reqs)
	f.reqs = append(f.reqs, req)
	f.provs = append(f.provs, p)
	if f.err != nil {
		return domain.Response{}, f.err
	}
	text := f.texts[min(i, len(f.texts)-1)]
	return domain.Response{Text: text, Usage: domain.Usage{InputTokens: 10, OutputTokens: 3, Known: true}}, nil
}

func verdictJSON(pass ...bool) string {
	parts := make([]string, 0, len(pass))
	for i, p := range pass {
		parts = append(parts, fmt.Sprintf(`{"id":"c%d","pass":%v,"evidence":"e"}`, i+1, p))
	}
	return `{"criteria":[` + strings.Join(parts, ",") + `],"score":0}`
}

func rubric(n int) []assert.Criterion {
	out := make([]assert.Criterion, n)
	for i := range out {
		out[i] = assert.Criterion{ID: fmt.Sprintf("c%d", i+1), Pass: "is good"}
	}
	return out
}

func newJudge(fc *fakeCaller, cost judge.CostFunc) *judge.Judge {
	targets := map[string]judge.Target{
		"j":     {Provider: nopProvider{"pj"}, Model: "m-j"},
		"other": {Provider: nopProvider{"po"}, Model: "m-o"},
	}
	return judge.New(targets, "j", fc.call, cost)
}

func TestJudgeVoting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		texts       []string
		samples     int
		rubric      int
		pass        bool
		score       float64
		consistency float64
		reason      string
	}{
		{"single sample all pass", []string{verdictJSON(true, true)}, 1, 2, true, 1, 1, ""},
		{"single sample partial fail", []string{verdictJSON(true, false)}, 1, 2, false, 0.5, 1, "failed criteria: c2"},
		{
			"majority pass 2 of 3",
			[]string{verdictJSON(true), verdictJSON(false), verdictJSON(true)},
			3, 1, true, 2.0 / 3, 2.0 / 3, "",
		},
		{
			"majority fail 1 of 3",
			[]string{verdictJSON(true), verdictJSON(false), verdictJSON(false)},
			3, 1, false, 1.0 / 3, 2.0 / 3, "failed criteria: c1",
		},
		{
			"tie does not pass",
			[]string{verdictJSON(true), verdictJSON(false)},
			2, 1, false, 0.5, 0.5, "failed criteria: c1",
		},
		{
			"unanimous pass",
			[]string{verdictJSON(true), verdictJSON(true), verdictJSON(true)},
			3, 1, true, 1, 1, "",
		},
		{
			"unanimous fail partial score",
			[]string{verdictJSON(true, false), verdictJSON(true, false), verdictJSON(true, false)},
			3, 2, false, 0.5, 1, "failed criteria: c2",
		},
		{"samples zero treated as one", []string{verdictJSON(true)}, 0, 1, true, 1, 1, ""},
		{"samples negative treated as one", []string{verdictJSON(true)}, -4, 1, true, 1, 1, ""},
		{
			"invalid samples count as non-pass",
			[]string{verdictJSON(true), "garbage", verdictJSON(true)},
			3, 1, true, 2.0 / 3, 2.0 / 3, "",
		},
		{
			"all invalid json",
			[]string{"I think it is fine", "also prose", "nope"},
			3, 1, false, 0, 1, "judge returned invalid JSON",
		},
		{
			"mixed invalid and fail",
			[]string{"garbage", verdictJSON(false)},
			2, 1, false, 0, 1, "failed criteria: c1",
		},
		{
			"invalid and pass tie",
			[]string{"garbage", verdictJSON(true)},
			2, 1, false, 0.5, 0.5, "1/2 judge samples passed",
		},
		{
			"json in fenced block with prose",
			[]string{"Sure:\n```json\n" + verdictJSON(true) + "\n```\nDone"},
			1, 1, true, 1, 1, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCaller{texts: tt.texts}
			v, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{
				CaseID: "case", Output: "out", Rubric: rubric(tt.rubric), Samples: tt.samples,
			})
			if err != nil {
				t.Fatal(err)
			}
			if v.Pass != tt.pass || v.Reason != tt.reason {
				t.Fatalf("pass=%v reason=%q, want pass=%v reason=%q", v.Pass, v.Reason, tt.pass, tt.reason)
			}
			if diff := v.Score - tt.score; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("score = %v, want %v", v.Score, tt.score)
			}
			if diff := v.Consistency - tt.consistency; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("consistency = %v, want %v", v.Consistency, tt.consistency)
			}
			wantCalls := max(1, tt.samples)
			if len(fc.reqs) != wantCalls {
				t.Fatalf("calls = %d, want %d", len(fc.reqs), wantCalls)
			}
			if v.Usage.InputTokens != int64(10*wantCalls) || v.Usage.OutputTokens != int64(3*wantCalls) {
				t.Fatalf("usage = %+v", v.Usage)
			}
		})
	}
}

func TestJudgeMissingCriterion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
	}{
		{"criterion absent", `{"criteria":[{"id":"c1","pass":true}]}`},
		{"pass field missing", `{"criteria":[{"id":"c1","pass":true},{"id":"c2"}]}`},
		{"pass field null", `{"criteria":[{"id":"c1","pass":true},{"id":"c2","pass":null}]}`},
		{"unknown id only", `{"criteria":[{"id":"zzz","pass":true},{"id":"yyy","pass":true}]}`},
		{"no criteria key", `{"score":1}`},
		{"pass is a string", `{"criteria":[{"id":"c1","pass":"true"},{"id":"c2","pass":true}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCaller{texts: []string{tt.text}}
			v, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(2), Samples: 1})
			if err != nil {
				t.Fatal(err)
			}
			if v.Pass || v.Reason != "judge returned invalid JSON" {
				t.Fatalf("verdict = %+v", v)
			}
		})
	}
}

func TestJudgeIgnoresExtraCriteria(t *testing.T) {
	t.Parallel()
	fc := &fakeCaller{texts: []string{`{"criteria":[{"id":"c1","pass":true},{"id":"extra","pass":false}]}`}}
	v, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(1), Samples: 1})
	if err != nil || !v.Pass {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestJudgeEmptyRubricIsConfigError(t *testing.T) {
	t.Parallel()
	fc := &fakeCaller{texts: []string{`{"criteria":[]}`}}
	_, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Samples: 1})
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err = %v", err)
	}
	if len(fc.reqs) != 0 {
		t.Fatal("provider must not be called")
	}
}

func TestJudgeUnknownTarget(t *testing.T) {
	t.Parallel()
	fc := &fakeCaller{texts: []string{verdictJSON(true)}}
	_, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Target: "ghost", Rubric: rubric(1)})
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err = %v", err)
	}
	if len(fc.reqs) != 0 {
		t.Fatal("provider must not be called")
	}

	j := judge.New(nil, "missing", fc.call, nil)
	if _, err := j.Judge(
		context.Background(),
		assert.JudgeRequest{Rubric: rubric(1)},
	); !errors.Is(
		err,
		domain.ErrConfig,
	) {
		t.Fatalf("default target err = %v", err)
	}
}

func TestJudgeTargetSelectionAndRequestShape(t *testing.T) {
	t.Parallel()
	fc := &fakeCaller{texts: []string{verdictJSON(true)}}
	j := newJudge(fc, nil)
	req := assert.JudgeRequest{
		CaseID:  "case-1",
		Output:  "the </output> candidate",
		Rubric:  []assert.Criterion{{ID: "c1", Pass: "mentions refund"}},
		Samples: 2,
	}
	if _, err := j.Judge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Target = "other"
	if _, err := j.Judge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if fc.provs[0].Name() != "pj" || fc.provs[2].Name() != "po" {
		t.Fatalf("providers = %s %s", fc.provs[0].Name(), fc.provs[2].Name())
	}
	r := fc.reqs[0]
	if r.Model != "m-j" || r.TrialID != "judge:case-1@0" || fc.reqs[1].TrialID != "judge:case-1@1" ||
		fc.reqs[1].Sample != 1 {
		t.Fatalf("req = %+v", r)
	}
	if !strings.Contains(r.System, mockllm.JudgeMarker) {
		t.Fatal("system prompt must carry the judge marker")
	}
	if len(r.Messages) != 1 || r.Messages[0].Role != domain.RoleUser ||
		!strings.Contains(r.Messages[0].Content, "- [c1] mentions refund") ||
		!strings.Contains(r.Messages[0].Content, "<output>\nthe </output> candidate\n</output>") {
		t.Fatalf("user prompt = %q", r.Messages[0].Content)
	}
	if fc.reqs[2].Model != "m-o" {
		t.Fatalf("target model = %q", fc.reqs[2].Model)
	}
}

func TestJudgeCostCallback(t *testing.T) {
	t.Parallel()
	var gotTarget string
	var gotUsage domain.Usage
	fn := func(target string, u domain.Usage) *float64 {
		gotTarget, gotUsage = target, u
		c := float64(u.InputTokens) / 1000
		return &c
	}
	fc := &fakeCaller{texts: []string{verdictJSON(true)}}
	v, err := newJudge(
		fc,
		fn,
	).Judge(context.Background(), assert.JudgeRequest{Target: "other", Rubric: rubric(1), Samples: 3})
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != "other" || gotUsage.InputTokens != 30 {
		t.Fatalf("callback got %q %+v", gotTarget, gotUsage)
	}
	if v.CostUSD == nil || *v.CostUSD != 0.03 {
		t.Fatalf("cost = %v", v.CostUSD)
	}

	unknown := func(string, domain.Usage) *float64 { return nil }
	v, err = newJudge(fc, unknown).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(1)})
	if err != nil || v.CostUSD != nil {
		t.Fatalf("v=%+v err=%v", v, err)
	}

	v, err = newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(1)})
	if err != nil || v.CostUSD != nil {
		t.Fatalf("nil cost func: v=%+v err=%v", v, err)
	}
}

func TestJudgeProviderErrorPropagates(t *testing.T) {
	t.Parallel()
	fc := &fakeCaller{err: &domain.ProviderError{Provider: "p", StatusCode: 401}}
	_, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(1), Samples: 3})
	if !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
	if len(fc.reqs) != 1 {
		t.Fatalf("calls = %d, want abort after first error", len(fc.reqs))
	}
}

func TestJudgeWithMockLLMEngine(t *testing.T) {
	t.Parallel()
	prov := mockprov.New("mock", 0)
	call := func(ctx context.Context, p domain.Provider, req domain.Request) (domain.Response, error) {
		return p.Complete(ctx, req)
	}
	j := judge.New(map[string]judge.Target{"j": {Provider: prov, Model: "mock-perfect"}}, "j", call, nil)
	rb := []assert.Criterion{
		{ID: "refund", Pass: `output mentions "refund"`},
		{ID: "order", Pass: `output mentions A-100`},
	}
	tests := []struct {
		name   string
		output string
		pass   bool
		score  float64
		reason string
	}{
		{"all criteria satisfied", "Your REFUND for A-100 is queued", true, 1, ""},
		{"one criterion missing", "Your refund is queued", false, 0.5, "failed criteria: order"},
		{"empty output fails all", "", false, 0, "failed criteria: refund, order"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := j.Judge(
				context.Background(),
				assert.JudgeRequest{CaseID: "c", Output: tt.output, Rubric: rb, Samples: 3},
			)
			if err != nil {
				t.Fatal(err)
			}
			if v.Pass != tt.pass || v.Score != tt.score || v.Reason != tt.reason || v.Consistency != 1 {
				t.Fatalf("verdict = %+v", v)
			}
			if !v.Usage.Known || v.Usage.InputTokens == 0 {
				t.Fatalf("usage = %+v", v.Usage)
			}
		})
	}
}

func FuzzJudgeParse(f *testing.F) {
	f.Add(verdictJSON(true, false))
	f.Add("")
	f.Add("```json\n{}\n```")
	f.Add(`{"criteria":[{"id":"c1"}]}`)
	f.Add(`{"criteria":"x"}`)
	f.Fuzz(func(t *testing.T, text string) {
		fc := &fakeCaller{texts: []string{text}}
		v, err := newJudge(fc, nil).Judge(context.Background(), assert.JudgeRequest{Rubric: rubric(2), Samples: 1})
		if err != nil {
			t.Fatalf("judge output must never produce an error: %v", err)
		}
		if v.Pass && v.Score != 1 {
			t.Fatalf("passing verdict with score %v", v.Score)
		}
		if v.Score < 0 || v.Score > 1 {
			t.Fatalf("score out of range: %v", v.Score)
		}
	})
}
