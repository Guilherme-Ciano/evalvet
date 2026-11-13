package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/provider"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

type Config struct {
	Name       string
	Compatible bool
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type Provider struct {
	cfg    Config
	client oai.Client
}

func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithHTTPClient(cfg.HTTPClient),
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{cfg: cfg, client: oai.NewClient(opts...)}
}

func (p *Provider) Name() string { return p.cfg.Name }

func (p *Provider) Kind() string {
	if p.cfg.Compatible {
		return "openai_compatible"
	}
	return "openai"
}

func (p *Provider) Capabilities() domain.Capabilities {
	return domain.Capabilities{Temperature: true, Seed: true, Tools: true, Streaming: true}
}

func (p *Provider) params(req domain.Request) oai.ChatCompletionNewParams {
	msgs := make([]oai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		msgs = append(msgs, convertMessage(m)...)
	}
	params := oai.ChatCompletionNewParams{
		Model:         req.Model,
		Messages:      msgs,
		StreamOptions: oai.ChatCompletionStreamOptionsParam{IncludeUsage: oai.Bool(true)},
	}
	if req.Params.MaxTokens > 0 {
		if p.cfg.Compatible {
			params.MaxTokens = oai.Int(req.Params.MaxTokens)
		} else {
			params.MaxCompletionTokens = oai.Int(req.Params.MaxTokens)
		}
	}
	if req.Params.Seed != nil {
		params.Seed = oai.Int(*req.Params.Seed)
	}
	if req.Params.Temperature != nil {
		params.Temperature = oai.Float(*req.Params.Temperature)
	}
	if req.Params.ReasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(req.Params.ReasoningEffort)
	}
	for _, t := range req.Tools {
		var schema map[string]any
		if len(t.Parameters) > 0 {
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				schema = nil
			}
		}
		def := shared.FunctionDefinitionParam{Name: t.Name, Parameters: shared.FunctionParameters(schema)}
		if t.Description != "" {
			def.Description = oai.String(t.Description)
		}
		params.Tools = append(params.Tools, oai.ChatCompletionFunctionTool(def))
	}
	return params
}

func convertMessage(m domain.Message) []oai.ChatCompletionMessageParamUnion {
	switch m.Role {
	case domain.RoleAssistant:
		a := oai.ChatCompletionAssistantMessageParam{}
		if m.Content != "" {
			a.Content.OfString = oai.String(m.Content)
		}
		for _, c := range m.ToolCalls {
			a.ToolCalls = append(a.ToolCalls, oai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &oai.ChatCompletionMessageFunctionToolCallParam{
					ID: c.ID,
					Function: oai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      c.Name,
						Arguments: string(c.Arguments),
					},
				},
			})
		}
		return []oai.ChatCompletionMessageParamUnion{{OfAssistant: &a}}
	case domain.RoleTool:
		out := make([]oai.ChatCompletionMessageParamUnion, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			out = append(out, oai.ToolMessage(r.Content, r.CallID))
		}
		return out
	default:
		return []oai.ChatCompletionMessageParamUnion{oai.UserMessage(m.Content)}
	}
}

type toolAcc struct {
	id, name string
	args     strings.Builder
}

type streamState struct {
	start   time.Time
	text    strings.Builder
	refusal strings.Builder
	tools   map[int64]*toolAcc
	finish  string
	usage   domain.Usage
	first   time.Duration
	model   string
}

func (s *streamState) usageFrom(u oai.CompletionUsage) {
	cached := u.PromptTokensDetails.CachedTokens
	s.usage = domain.Usage{
		InputTokens:       u.PromptTokens - cached,
		CachedInputTokens: cached,
		CacheWriteTokens:  u.PromptTokensDetails.CacheWriteTokens,
		OutputTokens:      u.CompletionTokens,
		ReasoningTokens:   u.CompletionTokensDetails.ReasoningTokens,
		Known:             true,
	}
}

func (s *streamState) add(chunk *oai.ChatCompletionChunk) {
	if chunk.Model != "" {
		s.model = chunk.Model
	}
	if chunk.JSON.Usage.Valid() && chunk.Usage.TotalTokens > 0 {
		s.usageFrom(chunk.Usage)
	}
	if len(chunk.Choices) == 0 {
		return
	}
	choice := &chunk.Choices[0]
	d := &choice.Delta
	if s.first == 0 && (d.Content != "" || len(d.ToolCalls) > 0 || d.Refusal != "") {
		s.first = time.Since(s.start)
	}
	s.text.WriteString(d.Content)
	s.refusal.WriteString(d.Refusal)
	for i := range d.ToolCalls {
		tc := &d.ToolCalls[i]
		acc, ok := s.tools[tc.Index]
		if !ok {
			acc = &toolAcc{}
			s.tools[tc.Index] = acc
		}
		if tc.ID != "" {
			acc.id = tc.ID
		}
		if tc.Function.Name != "" {
			acc.name = tc.Function.Name
		}
		acc.args.WriteString(tc.Function.Arguments)
	}
	if choice.FinishReason != "" {
		s.finish = choice.FinishReason
	}
}

func (s *streamState) response(ttfb time.Duration) domain.Response {
	refused := s.refusal.Len() > 0
	resp := domain.Response{
		Text:       s.text.String(),
		StopReason: stopReason(s.finish, refused),
		Usage:      s.usage,
		Model:      s.model,
	}
	if refused && resp.Text == "" {
		resp.Text = s.refusal.String()
	}
	idx := slices.Sorted(maps.Keys(s.tools))
	for _, i := range idx {
		acc := s.tools[i]
		args := acc.args.String()
		if args == "" {
			args = "{}"
		}
		resp.ToolCalls = append(
			resp.ToolCalls,
			domain.ToolCall{ID: acc.id, Name: acc.name, Arguments: json.RawMessage(args)},
		)
	}
	total := time.Since(s.start)
	first := s.first
	if first == 0 {
		first = total
	}
	resp.Timing = domain.Timing{Start: s.start, TTFB: ttfb, FirstToken: first, Total: total}
	return resp
}

func (p *Provider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	ctx, rec := transport.WithRecorder(ctx)
	st := &streamState{start: time.Now(), tools: map[int64]*toolAcc{}}
	var opts []option.RequestOption
	if req.TrialID != "" {
		opts = append(opts, option.WithHeader(mockllm.TrialHeader, req.TrialID))
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, p.params(req), opts...)
	defer stream.Close()
	for stream.Next() {
		chunk := stream.Current()
		st.add(&chunk)
	}
	if err := stream.Err(); err != nil {
		return domain.Response{}, p.mapError(err)
	}
	if st.finish == "" {
		return domain.Response{}, provider.Truncated(p.cfg.Name)
	}
	return st.response(rec.TTFB()), nil
}

func stopReason(finish string, refused bool) domain.StopReason {
	if refused {
		return domain.StopRefusal
	}
	switch finish {
	case "stop":
		return domain.StopEnd
	case "tool_calls", "function_call":
		return domain.StopToolUse
	case "length":
		return domain.StopMaxTokens
	case "content_filter":
		return domain.StopRefusal
	default:
		return domain.StopOther
	}
}

func (p *Provider) mapError(err error) error {
	if apiErr, ok := errors.AsType[*oai.Error](err); ok {
		var h http.Header
		if apiErr.Response != nil {
			h = apiErr.Response.Header
		}
		return provider.HTTPError(p.cfg.Name, apiErr.StatusCode, h, apiErr.Message)
	}
	return fmt.Errorf("%s stream: %w", p.cfg.Name, err)
}
