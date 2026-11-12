package config

import "time"

const (
	KindOpenAI           = "openai"
	KindOpenAICompatible = "openai_compatible"
	KindAnthropic        = "anthropic"
	KindMock             = "mock"
)

type Suite struct {
	Version     int                 `yaml:"version"`
	Name        string              `yaml:"suite"`
	Description string              `yaml:"description"`
	Defaults    Defaults            `yaml:"defaults"`
	Providers   map[string]Provider `yaml:"providers"`
	Targets     map[string]Target   `yaml:"targets"`
	Prompts     map[string]string   `yaml:"prompts"`
	Tools       map[string]Tool     `yaml:"tools"`
	Tests       []Test              `yaml:"tests"`
	Matrix      Matrix              `yaml:"matrix"`
	Compare     []CompareSpec       `yaml:"compare"`
	Gates       Gates               `yaml:"gates"`

	File  string    `yaml:"-"`
	Dir   string    `yaml:"-"`
	Cases []Case    `yaml:"-"`
	Hash  string    `yaml:"-"`
	lines lineIndex `yaml:"-"`
}

type Defaults struct {
	Samples   int           `yaml:"samples"`
	Timeout   time.Duration `yaml:"timeout"`
	MaxTokens int64         `yaml:"max_tokens"`
	Judge     JudgeDefaults `yaml:"judge"`
}

type JudgeDefaults struct {
	Target  string `yaml:"target"`
	Samples int    `yaml:"samples"`
}

type Limits struct {
	RPM         int `yaml:"rpm"`
	TPM         int `yaml:"tpm"`
	Concurrency int `yaml:"concurrency"`
}

type Provider struct {
	Kind      string `yaml:"kind"`
	APIKeyEnv string `yaml:"api_key_env"`
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url"`
	Limits    Limits `yaml:"limits"`
}

type AnthropicParams struct {
	Effort string `yaml:"effort"`
}

type OpenAIParams struct {
	ReasoningEffort string `yaml:"reasoning_effort"`
}

type TargetParams struct {
	MaxTokens   int64           `yaml:"max_tokens"`
	Seed        *int64          `yaml:"seed"`
	Temperature *float64        `yaml:"temperature"`
	Anthropic   AnthropicParams `yaml:"anthropic"`
	OpenAI      OpenAIParams    `yaml:"openai"`
}

type Target struct {
	Provider string       `yaml:"provider"`
	Model    string       `yaml:"model"`
	Params   TargetParams `yaml:"params"`
}

type MockMatch struct {
	When    map[string]any `yaml:"when"`
	Return  any            `yaml:"return"`
	IsError bool           `yaml:"is_error"`
}

type MockSpec struct {
	Static    any         `yaml:"static"`
	Match     []MockMatch `yaml:"match"`
	Default   any         `yaml:"default"`
	Sequence  []any       `yaml:"sequence"`
	Error     string      `yaml:"error"`
	LatencyMS int         `yaml:"latency_ms"`
}

type Tool struct {
	Description string   `yaml:"description"`
	Parameters  any      `yaml:"parameters"`
	Mock        MockSpec `yaml:"mock"`
}

type Turn struct {
	User string `yaml:"user"`
}

type AgentSpec struct {
	Tools        []string `yaml:"tools"`
	MaxSteps     int      `yaml:"max_steps"`
	MaxToolCalls int      `yaml:"max_tool_calls"`
}

type Times struct {
	Min *int `yaml:"min"`
	Max *int `yaml:"max"`
}

type Criterion struct {
	ID   string `yaml:"id"`
	Pass string `yaml:"pass"`
}

type AssertSpec struct {
	Kind            string         `yaml:"kind"`
	Not             *AssertSpec    `yaml:"not"`
	Value           any            `yaml:"value"`
	Any             bool           `yaml:"any"`
	CaseInsensitive bool           `yaml:"case_insensitive"`
	Pattern         string         `yaml:"pattern"`
	Schema          any            `yaml:"schema"`
	Path            string         `yaml:"path"`
	Equals          any            `yaml:"equals"`
	Regex           string         `yaml:"regex"`
	Exists          *bool          `yaml:"exists"`
	Name            string         `yaml:"name"`
	Args            map[string]any `yaml:"args"`
	Times           Times          `yaml:"times"`
	Sequence        []string       `yaml:"sequence"`
	Strict          bool           `yaml:"strict"`
	Rubric          []Criterion    `yaml:"rubric"`
	Samples         int            `yaml:"samples"`
	Target          string         `yaml:"target"`
	MaxUSD          float64        `yaml:"max_usd"`
	MaxMS           int64          `yaml:"max_ms"`
	Weight          float64        `yaml:"weight"`
}

type Test struct {
	ID      string         `yaml:"id"`
	Prompt  string         `yaml:"prompt"`
	System  string         `yaml:"system"`
	Vars    map[string]any `yaml:"vars"`
	Each    string         `yaml:"each"`
	Turns   []Turn         `yaml:"turns"`
	Agent   *AgentSpec     `yaml:"agent"`
	Samples int            `yaml:"samples"`
	Assert  []AssertSpec   `yaml:"assert"`
}

type Matrix struct {
	Targets []string `yaml:"targets"`
}

type CompareSpec struct {
	Base   string  `yaml:"base"`
	Head   string  `yaml:"head"`
	Mode   string  `yaml:"mode"`
	Margin float64 `yaml:"margin"`
}

type CostGate struct {
	MaxIncreasePct *float64 `yaml:"max_increase_pct"`
}

type Gates struct {
	Baseline     string   `yaml:"baseline"`
	Alpha        float64  `yaml:"alpha"`
	MinEffect    float64  `yaml:"min_effect"`
	MinPassRate  *float64 `yaml:"min_pass_rate"`
	Cost         CostGate `yaml:"cost"`
	MaxErrorRate *float64 `yaml:"max_error_rate"`
}

type Case struct {
	ID       string
	TestIdx  int
	Line     int
	System   string
	Messages []string
	Agent    *AgentSpec
	Samples  int
	Asserts  []AssertSpec
	Data     map[string]any
}
