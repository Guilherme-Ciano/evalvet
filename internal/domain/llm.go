package domain

import (
	"context"
	"encoding/json"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type StopReason string

const (
	StopEnd       StopReason = "end"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopOther     StopReason = "other"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
}

type Message struct {
	Role        Role         `json:"role"`
	Content     string       `json:"content,omitempty"`
	ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ToolResult `json:"tool_results,omitempty"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Params struct {
	MaxTokens       int64          `json:"max_tokens,omitempty"`
	Seed            *int64         `json:"seed,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	Effort          string         `json:"effort,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`
}

type Request struct {
	Model    string    `json:"model"`
	System   string    `json:"system,omitempty"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Params   Params    `json:"params"`
	Sample   int       `json:"-"`
	TrialID  string    `json:"-"`
}

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens  int64 `json:"cache_write_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens,omitempty"`
	Known             bool  `json:"known"`
}

func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:       u.InputTokens + o.InputTokens,
		CachedInputTokens: u.CachedInputTokens + o.CachedInputTokens,
		CacheWriteTokens:  u.CacheWriteTokens + o.CacheWriteTokens,
		OutputTokens:      u.OutputTokens + o.OutputTokens,
		ReasoningTokens:   u.ReasoningTokens + o.ReasoningTokens,
		Known:             u.Known || o.Known,
	}
}

type Timing struct {
	Start      time.Time     `json:"-"`
	TTFB       time.Duration `json:"ttfb"`
	FirstToken time.Duration `json:"first_token"`
	Total      time.Duration `json:"total"`
}

type Response struct {
	Text       string     `json:"text"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	StopReason StopReason `json:"stop_reason"`
	Usage      Usage      `json:"usage"`
	Timing     Timing     `json:"timing"`
	Model      string     `json:"model,omitempty"`
	CacheHit   bool       `json:"-"`
}

type Provider interface {
	Name() string
	Kind() string
	Complete(ctx context.Context, req Request) (Response, error)
}

type Capabilities struct {
	Temperature bool
	Seed        bool
	Tools       bool
	Streaming   bool
}

type CapabilityReporter interface {
	Capabilities() Capabilities
}

func CapabilitiesOf(p Provider) Capabilities {
	if c, ok := p.(CapabilityReporter); ok {
		return c.Capabilities()
	}
	return Capabilities{Tools: true}
}
