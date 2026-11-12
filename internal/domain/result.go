package domain

import "time"

type AssertOutcome struct {
	Kind   string   `json:"kind"`
	Pass   bool     `json:"pass"`
	Score  *float64 `json:"score,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

type Step struct {
	Response    Response     `json:"response"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

type Trajectory struct {
	Steps []Step `json:"steps"`
}

func (t Trajectory) ToolCalls() []ToolCall {
	var out []ToolCall
	for _, s := range t.Steps {
		out = append(out, s.Response.ToolCalls...)
	}
	return out
}

type Trial struct {
	CaseID     string          `json:"case_id"`
	Target     string          `json:"target"`
	Sample     int             `json:"sample"`
	Pass       bool            `json:"pass"`
	Score      *float64        `json:"score,omitempty"`
	Errored    bool            `json:"errored,omitempty"`
	Error      string          `json:"error,omitempty"`
	Output     string          `json:"output"`
	Asserts    []AssertOutcome `json:"asserts,omitempty"`
	Usage      Usage           `json:"usage"`
	JudgeUsage Usage           `json:"judge_usage"`
	CostUSD    *float64        `json:"cost_usd,omitempty"`
	JudgeCost  *float64        `json:"judge_cost_usd,omitempty"`
	Latency    time.Duration   `json:"latency_ns"`
	TTFT       time.Duration   `json:"ttft_ns"`
	Cached     bool            `json:"cached"`
	Steps      int             `json:"steps"`
	ToolCalls  int             `json:"tool_calls"`
	Trajectory *Trajectory     `json:"trajectory,omitempty"`
}

func (t Trial) FirstFailure() string {
	if t.Errored {
		return "error: " + t.Error
	}
	for _, a := range t.Asserts {
		if !a.Pass {
			return a.Kind + ": " + a.Reason
		}
	}
	return ""
}

type CaseStats struct {
	CaseID       string   `json:"case_id"`
	Line         int      `json:"line,omitempty"`
	N            int      `json:"n"`
	Passes       int      `json:"passes"`
	Errors       int      `json:"errors"`
	MeanScore    *float64 `json:"mean_score,omitempty"`
	FirstFailure string   `json:"first_failure,omitempty"`
}

func (c CaseStats) PassRate() float64 {
	if c.N == 0 {
		return 0
	}
	return float64(c.Passes) / float64(c.N)
}

type TargetStats struct {
	Target       string        `json:"target"`
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Cases        []CaseStats   `json:"cases"`
	PassRate     float64       `json:"pass_rate"`
	Trials       int           `json:"trials"`
	Errors       int           `json:"errors"`
	CachedTrials int           `json:"cached_trials"`
	CostUSD      *float64      `json:"cost_usd,omitempty"`
	JudgeCostUSD *float64      `json:"judge_cost_usd,omitempty"`
	LatencyP50   time.Duration `json:"latency_p50_ns"`
	LatencyP95   time.Duration `json:"latency_p95_ns"`
	TTFTP50      time.Duration `json:"ttft_p50_ns"`
	LiveTrials   int           `json:"live_trials"`
	MeanToolCall float64       `json:"mean_tool_calls"`
}

func (t TargetStats) ErrorRate() float64 {
	if t.Trials == 0 {
		return 0
	}
	return float64(t.Errors) / float64(t.Trials)
}

type Verdict string

const (
	VerdictPass           Verdict = "pass"
	VerdictRegression     Verdict = "regression"
	VerdictImprovement    Verdict = "improvement"
	VerdictInconclusive   Verdict = "inconclusive"
	VerdictNonInferior    Verdict = "non_inferior"
	VerdictNotNonInferior Verdict = "not_non_inferior"
	VerdictNoBaseline     Verdict = "no_baseline"
)

func (v Verdict) Failing() bool {
	return v == VerdictRegression || v == VerdictNotNonInferior
}

type CaseDiff struct {
	CaseID       string  `json:"case_id"`
	Line         int     `json:"line,omitempty"`
	BasePasses   int     `json:"base_passes"`
	BaseN        int     `json:"base_n"`
	HeadPasses   int     `json:"head_passes"`
	HeadN        int     `json:"head_n"`
	P            float64 `json:"p"`
	PAdjusted    float64 `json:"p_adjusted"`
	FirstFailure string  `json:"first_failure,omitempty"`
}

type Comparison struct {
	Name         string        `json:"name"`
	Base         string        `json:"base"`
	Head         string        `json:"head"`
	Mode         string        `json:"mode"`
	N            int           `json:"n"`
	MeanK        float64       `json:"mean_k"`
	BasePassRate float64       `json:"base_pass_rate"`
	HeadPassRate float64       `json:"head_pass_rate"`
	Delta        float64       `json:"delta"`
	CILow        float64       `json:"ci_low"`
	CIHigh       float64       `json:"ci_high"`
	P            float64       `json:"p"`
	PImprove     float64       `json:"p_improve"`
	Exact        bool          `json:"exact"`
	Test         string        `json:"test"`
	MDE          float64       `json:"mde"`
	Verdict      Verdict       `json:"verdict"`
	Reasons      []string      `json:"reasons,omitempty"`
	BaseCost     *float64      `json:"base_cost_usd,omitempty"`
	HeadCost     *float64      `json:"head_cost_usd,omitempty"`
	BaseP50      time.Duration `json:"base_latency_p50_ns"`
	HeadP50      time.Duration `json:"head_latency_p50_ns"`
	BaseP95      time.Duration `json:"base_latency_p95_ns"`
	HeadP95      time.Duration `json:"head_latency_p95_ns"`
	BaseTTFTP50  time.Duration `json:"base_ttft_p50_ns"`
	HeadTTFTP50  time.Duration `json:"head_ttft_p50_ns"`
	Cases        []CaseDiff    `json:"cases,omitempty"`
}

type GateFailure struct {
	Gate    string `json:"gate"`
	Target  string `json:"target"`
	Message string `json:"message"`
}

type Method struct {
	Alpha          float64 `json:"alpha"`
	MinEffect      float64 `json:"min_effect"`
	Seed           uint64  `json:"seed"`
	Permutations   int     `json:"permutations"`
	Bootstrap      int     `json:"bootstrap"`
	PricingVersion string  `json:"pricing_version"`
}

type RunResult struct {
	Schema      int           `json:"schema"`
	Suite       string        `json:"suite"`
	SuiteFile   string        `json:"suite_file"`
	ConfigHash  string        `json:"config_hash"`
	Version     string        `json:"evalvet_version"`
	StartedAt   time.Time     `json:"started_at"`
	Duration    time.Duration `json:"duration_ns"`
	Method      Method        `json:"method"`
	Targets     []TargetStats `json:"targets"`
	Comparisons []Comparison  `json:"comparisons,omitempty"`
	Gates       []GateFailure `json:"gate_failures,omitempty"`
	TotalCost   *float64      `json:"total_cost_usd,omitempty"`
	CacheHits   int           `json:"cache_hits"`
	TotalTrials int           `json:"total_trials"`
	Warnings    []string      `json:"warnings,omitempty"`
	Trials      []Trial       `json:"trials,omitempty"`
}

func (r RunResult) Failing() bool {
	if len(r.Gates) > 0 {
		return true
	}
	for _, c := range r.Comparisons {
		if c.Verdict.Failing() {
			return true
		}
	}
	return false
}

func (r RunResult) Target(name string) (TargetStats, bool) {
	for _, t := range r.Targets {
		if t.Target == name {
			return t, true
		}
	}
	return TargetStats{}, false
}

type JudgeVerdict struct {
	Pass        bool
	Score       float64
	Reason      string
	Usage       Usage
	CostUSD     *float64
	Consistency float64
}
