package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

type Target struct {
	Provider domain.Provider
	Model    string
	Params   domain.Params
}

type CostFunc func(target string, u domain.Usage) *float64

type Judge struct {
	targets       map[string]Target
	defaultTarget string
	call          func(ctx context.Context, p domain.Provider, req domain.Request) (domain.Response, error)
	cost          CostFunc
}

type Caller func(ctx context.Context, p domain.Provider, req domain.Request) (domain.Response, error)

func New(targets map[string]Target, defaultTarget string, call Caller, cost CostFunc) *Judge {
	return &Judge{targets: targets, defaultTarget: defaultTarget, call: call, cost: cost}
}

func systemPrompt() string {
	return mockllm.JudgeMarker + " You grade a candidate output against a checklist. " +
		"For every criterion decide pass true or false using only the candidate output as evidence. " +
		`Reply with JSON only: {"criteria":[{"id":"<id>","pass":true,"evidence":"<short quote>"}],"score":<fraction passed>}.`
}

func userPrompt(req assert.JudgeRequest) string {
	var sb strings.Builder
	sb.WriteString("Criteria:\n")
	for _, c := range req.Rubric {
		fmt.Fprintf(&sb, "- [%s] %s\n", c.ID, c.Pass)
	}
	sb.WriteString("\nCandidate output:\n<output>\n")
	sb.WriteString(req.Output)
	sb.WriteString("\n</output>\n")
	return sb.String()
}

type verdict struct {
	Criteria []struct {
		ID       string `json:"id"`
		Pass     *bool  `json:"pass"`
		Evidence string `json:"evidence"`
	} `json:"criteria"`
}

func parse(text string, rubric []assert.Criterion) (passed int, failed []string, err error) {
	v, err := assert.ExtractJSON(text)
	if err != nil {
		return 0, nil, fmt.Errorf("judge output: %w", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return 0, nil, fmt.Errorf("judge output: %w", err)
	}
	var vd verdict
	if err := json.Unmarshal(b, &vd); err != nil {
		return 0, nil, fmt.Errorf("judge output: %w", err)
	}
	got := map[string]bool{}
	for _, c := range vd.Criteria {
		if c.Pass != nil {
			got[c.ID] = *c.Pass
		}
	}
	for _, r := range rubric {
		p, ok := got[r.ID]
		if !ok {
			return 0, nil, fmt.Errorf("judge output: criterion %q missing: %w", r.ID, errInvalid)
		}
		if p {
			passed++
		} else {
			failed = append(failed, r.ID)
		}
	}
	return passed, failed, nil
}

func (j *Judge) Judge(ctx context.Context, req assert.JudgeRequest) (domain.JudgeVerdict, error) {
	name := req.Target
	if name == "" {
		name = j.defaultTarget
	}
	t, ok := j.targets[name]
	if !ok {
		return domain.JudgeVerdict{}, fmt.Errorf("judge target %q: %w", name, domain.ErrConfig)
	}
	samples := max(1, req.Samples)
	var (
		out        domain.JudgeVerdict
		votes      int
		scoreSum   float64
		lastFailed []string
		invalid    int
	)
	for i := range samples {
		r := domain.Request{
			Model:    t.Model,
			System:   systemPrompt(),
			Messages: []domain.Message{{Role: domain.RoleUser, Content: userPrompt(req)}},
			Params:   t.Params,
			Sample:   i,
			TrialID:  "judge:" + req.CaseID + "@" + fmt.Sprint(i),
		}
		resp, err := j.call(ctx, t.Provider, r)
		if err != nil {
			return out, err
		}
		out.Usage = out.Usage.Add(resp.Usage)
		passed, failed, err := parse(resp.Text, req.Rubric)
		if err != nil {
			invalid++
			continue
		}
		scoreSum += float64(passed) / float64(len(req.Rubric))
		if len(failed) == 0 {
			votes++
		} else {
			lastFailed = failed
		}
	}
	out.Pass = votes*2 > samples
	out.Score = scoreSum / float64(samples)
	agree := votes
	if !out.Pass {
		agree = samples - votes
	}
	out.Consistency = float64(agree) / float64(samples)
	switch {
	case out.Pass:
	case invalid == samples:
		out.Reason = "judge returned invalid JSON"
	case len(lastFailed) > 0:
		out.Reason = "failed criteria: " + strings.Join(lastFailed, ", ")
	default:
		out.Reason = fmt.Sprintf("%d/%d judge samples passed", votes, samples)
	}
	if j.cost != nil {
		out.CostUSD = j.cost(name, out.Usage)
	}
	return out, nil
}
