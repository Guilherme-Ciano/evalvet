package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
		Model:         shared.ChatModel(req.Model),
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

func (p *Provider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	ctx, rec := transport.WithRecorder(ctx)
	start := time.Now()
	opts := []option.RequestOption{}
	if req.TrialID != "" {
		opts = append(opts, option.WithHeader(mockllm.TrialHeader, req.TrialID))
	}
	stream := p.client.Chat.Completions.NewStreaming(ctx, p.params(req), opts...)
	defer stream.Close()
	var (
		text    strings.Builder
		refusal strings.Builder
		tools   = map[int64]*toolAcc{}
		finish  string
		usage   domain.Usage
		first   time.Duration
		model   string
	)
	for stream.Next() {
		chunk := stream.Current()
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.JSON.Usage.Valid() && chunk.Usage.TotalTokens > 0 {
			usage = domain.Usage{
				InputTokens:       chunk.Usage.PromptTokens - chunk.Usage.PromptTokensDetails.CachedTokens,
				CachedInputTokens: chunk.Usage.PromptTokensDetails.CachedTokens,
				CacheWriteTokens:  chunk.Usage.PromptTokensDetails.CacheWriteTokens,
				OutputTokens:      chunk.Usage.CompletionTokens,
				ReasoningTokens:   chunk.Usage.CompletionTokensDetails.ReasoningTokens,
				Known:             true,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		d := choice.Delta
		if first == 0 && (d.Content != "" || len(d.ToolCalls) > 0 || d.Refusal != "") {
			first = time.Since(start)
		}
		text.WriteString(d.Content)
		refusal.WriteString(d.Refusal)
		for _, tc := range d.ToolCalls {
			acc, ok := tools[tc.Index]
			if !ok {
				acc = &toolAcc{}
				tools[tc.Index] = acc
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
			finish = choice.FinishReason
		}
	}
	if err := stream.Err(); err != nil {
		return domain.Response{}, p.mapError(err)
	}
	resp := domain.Response{
		Text:       text.String(),
		StopReason: stopReason(finish, refusal.Len() > 0),
		Usage:      usage,
		Model:      model,
	}
	if refusal.Len() > 0 && resp.Text == "" {
		resp.Text = refusal.String()
	}
	idx := make([]int64, 0, len(tools))
	for i := range tools {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return idx[a] < idx[b] })
	for _, i := range idx {
		acc := tools[i]
		args := acc.args.String()
		if args == "" {
			args = "{}"
		}
		resp.ToolCalls = append(resp.ToolCalls, domain.ToolCall{ID: acc.id, Name: acc.name, Arguments: json.RawMessage(args)})
	}
	total := time.Since(start)
	if first == 0 {
		first = total
	}
	resp.Timing = domain.Timing{Start: start, TTFB: rec.TTFB(), FirstToken: first, Total: total}
	return resp, nil
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
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
		var h http.Header
		if apiErr.Response != nil {
			h = apiErr.Response.Header
		}
		return provider.HTTPError(p.cfg.Name, apiErr.StatusCode, h, apiErr.Message)
	}
	return fmt.Errorf("%s stream: %w", p.cfg.Name, err)
}
