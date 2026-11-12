package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	ant "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/provider"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

type Config struct {
	Name       string
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type Provider struct {
	cfg    Config
	client ant.Client
}

func New(cfg Config) *Provider {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithMaxRetries(0),
		option.WithHTTPClient(cfg.HTTPClient),
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Provider{cfg: cfg, client: ant.NewClient(opts...)}
}

func (p *Provider) Name() string { return p.cfg.Name }
func (p *Provider) Kind() string { return "anthropic" }

func (p *Provider) Capabilities() domain.Capabilities {
	return domain.Capabilities{Tools: true, Streaming: true}
}

func (p *Provider) params(req domain.Request) ant.MessageNewParams {
	params := ant.MessageNewParams{
		Model:     req.Model,
		MaxTokens: req.Params.MaxTokens,
	}
	if params.MaxTokens <= 0 {
		params.MaxTokens = 1024
	}
	if req.System != "" {
		params.System = []ant.TextBlockParam{{Text: req.System}}
	}
	if req.Params.Effort != "" {
		params.OutputConfig = ant.OutputConfigParam{Effort: ant.OutputConfigEffort(req.Params.Effort)}
	}
	for _, m := range req.Messages {
		params.Messages = append(params.Messages, convertMessage(m))
	}
	for _, t := range req.Tools {
		schema := ant.ToolInputSchemaParam{}
		var raw map[string]any
		if len(t.Parameters) > 0 && json.Unmarshal(t.Parameters, &raw) == nil {
			schema.Properties = raw["properties"]
			if req, ok := raw["required"].([]any); ok {
				for _, r := range req {
					if s, ok := r.(string); ok {
						schema.Required = append(schema.Required, s)
					}
				}
			}
			extra := map[string]any{}
			for k, v := range raw {
				if k != "properties" && k != "required" && k != "type" {
					extra[k] = v
				}
			}
			if len(extra) > 0 {
				schema.ExtraFields = extra
			}
		}
		tool := ant.ToolUnionParamOfTool(schema, t.Name)
		if t.Description != "" {
			tool.OfTool.Description = ant.String(t.Description)
		}
		params.Tools = append(params.Tools, tool)
	}
	return params
}

func convertMessage(m domain.Message) ant.MessageParam {
	switch m.Role {
	case domain.RoleAssistant:
		var blocks []ant.ContentBlockParamUnion
		if m.Content != "" {
			blocks = append(blocks, ant.NewTextBlock(m.Content))
		}
		for _, c := range m.ToolCalls {
			var input any = map[string]any{}
			if len(c.Arguments) > 0 {
				input = json.RawMessage(c.Arguments)
			}
			blocks = append(blocks, ant.NewToolUseBlock(c.ID, input, c.Name))
		}
		return ant.NewAssistantMessage(blocks...)
	case domain.RoleTool:
		blocks := make([]ant.ContentBlockParamUnion, 0, len(m.ToolResults))
		for _, r := range m.ToolResults {
			blocks = append(blocks, ant.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		}
		return ant.NewUserMessage(blocks...)
	default:
		return ant.NewUserMessage(ant.NewTextBlock(m.Content))
	}
}

func (p *Provider) Complete(ctx context.Context, req domain.Request) (domain.Response, error) {
	ctx, rec := transport.WithRecorder(ctx)
	start := time.Now()
	var opts []option.RequestOption
	if req.TrialID != "" {
		opts = append(opts, option.WithHeader(mockllm.TrialHeader, req.TrialID))
	}
	stream := p.client.Messages.NewStreaming(ctx, p.params(req), opts...)
	defer stream.Close()
	var (
		msg   ant.Message
		first time.Duration
	)
	for stream.Next() {
		ev := stream.Current()
		if first == 0 && ev.Type == "content_block_delta" &&
			(ev.Delta.Type == "text_delta" || ev.Delta.Type == "input_json_delta") {
			first = time.Since(start)
		}
		if err := msg.Accumulate(ev); err != nil {
			return domain.Response{}, fmt.Errorf("%s accumulate: %w", p.cfg.Name, err)
		}
	}
	if err := stream.Err(); err != nil {
		return domain.Response{}, p.mapError(err)
	}
	resp := domain.Response{Model: msg.Model, StopReason: stopReason(msg.StopReason)}
	var text strings.Builder
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			resp.ToolCalls = append(resp.ToolCalls, domain.ToolCall{ID: b.ID, Name: b.Name, Arguments: args})
		}
	}
	resp.Text = text.String()
	if resp.StopReason == domain.StopRefusal && resp.Text == "" {
		resp.Text = msg.StopDetails.Explanation
	}
	u := msg.Usage
	resp.Usage = domain.Usage{
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  u.CacheCreationInputTokens,
		OutputTokens:      u.OutputTokens,
		ReasoningTokens:   u.OutputTokensDetails.ThinkingTokens,
		Known:             true,
	}
	total := time.Since(start)
	if first == 0 {
		first = total
	}
	resp.Timing = domain.Timing{Start: start, TTFB: rec.TTFB(), FirstToken: first, Total: total}
	return resp, nil
}

func stopReason(r ant.StopReason) domain.StopReason {
	switch r {
	case ant.StopReasonEndTurn, ant.StopReasonStopSequence:
		return domain.StopEnd
	case ant.StopReasonToolUse:
		return domain.StopToolUse
	case ant.StopReasonMaxTokens, ant.StopReasonModelContextWindowExceeded:
		return domain.StopMaxTokens
	case ant.StopReasonRefusal:
		return domain.StopRefusal
	default:
		return domain.StopOther
	}
}

func (p *Provider) mapError(err error) error {
	var apiErr *ant.Error
	if errors.As(err, &apiErr) {
		var h http.Header
		if apiErr.Response != nil {
			h = apiErr.Response.Header
		}
		return provider.HTTPError(p.cfg.Name, apiErr.StatusCode, h, apiErr.Message)
	}
	return fmt.Errorf("%s stream: %w", p.cfg.Name, err)
}
