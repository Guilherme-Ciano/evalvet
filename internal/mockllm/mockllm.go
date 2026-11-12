package mockllm

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const (
	JudgeMarker   = "You are an evaluation judge."
	RefusalMarker = "REFUSE_ME"
	TrialHeader   = "Evalvet-Trial-Id"
)

type profile struct {
	accuracy    float64
	wrongRefund float64
	skipPolicy  float64
}

func profileFor(model string) profile {
	switch model {
	case "mock-regressed":
		return profile{accuracy: 0.80, wrongRefund: 0.30, skipPolicy: 0.10}
	case "mock-perfect":
		return profile{accuracy: 1, wrongRefund: 0, skipPolicy: 0}
	case "mock-broken":
		return profile{accuracy: 0, wrongRefund: 1, skipPolicy: 1}
	default:
		return profile{accuracy: 0.95, wrongRefund: 0.02, skipPolicy: 0.01}
	}
}

type Engine struct {
	order *regexp.Regexp
	token *regexp.Regexp
}

func New() *Engine {
	return &Engine{
		order: regexp.MustCompile(`\b[A-Z]-\d{3,}\b`),
		token: regexp.MustCompile(`\b[A-Z]+-\d+\b|"[^"]+"`),
	}
}

func TrialID(caseID string, sample int) string {
	return caseID + "@" + strconv.Itoa(sample)
}

func splitTrial(trial string) (string, string) {
	i := strings.LastIndexByte(trial, '@')
	if i < 0 {
		return trial, "0"
	}
	return trial[:i], trial[i+1:]
}

func unit(parts ...string) float64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], h.Sum64())
	x := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(x) / float64(uint64(1)<<53)
}

func caseProbability(acc float64, caseID string) float64 {
	spread := math.Min(2*(1-acc), 0.4)
	p := acc + (0.5-unit("difficulty", caseID))*spread
	return math.Min(1, math.Max(0, p))
}

func seedFor(req domain.Request, trial string) string {
	if trial != "" {
		return trial
	}
	b, err := json.Marshal(req)
	if err != nil {
		return req.Model
	}
	return string(b)
}

func (e *Engine) Respond(req domain.Request, trial string) domain.Response {
	seed := seedFor(req, trial)
	caseID, sample := splitTrial(seed)
	var resp domain.Response
	switch {
	case strings.Contains(req.System, JudgeMarker):
		resp = e.judge(req)
	case strings.Contains(lastUser(req), RefusalMarker):
		resp = domain.Response{Text: "I can't help with that.", StopReason: domain.StopRefusal}
	case len(req.Tools) > 0:
		resp = e.agentStep(req, profileFor(req.Model), caseID, sample)
	case looksLikeInvoice(lastUser(req)):
		resp = extract(lastUser(req), profileFor(req.Model), caseID, sample)
	default:
		resp = domain.Response{Text: "mock answer: " + truncate(lastUser(req), 80), StopReason: domain.StopEnd}
	}
	resp.Model = req.Model
	resp.Usage = domain.Usage{
		InputTokens:  int64(estimateTokens(req)),
		OutputTokens: int64(max(1, (len(resp.Text)+argLen(resp.ToolCalls))/4)),
		Known:        true,
	}
	return resp
}

func argLen(calls []domain.ToolCall) int {
	n := 0
	for _, c := range calls {
		n += len(c.Name) + len(c.Arguments)
	}
	return n
}

func estimateTokens(req domain.Request) int {
	n := len(req.System)
	for _, m := range req.Messages {
		n += len(m.Content)
		for _, r := range m.ToolResults {
			n += len(r.Content)
		}
		n += argLen(m.ToolCalls)
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Parameters)
	}
	return max(1, n/4)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func lastUser(req domain.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == domain.RoleUser && req.Messages[i].Content != "" {
			return req.Messages[i].Content
		}
	}
	return ""
}

func firstUser(req domain.Request) string {
	for _, m := range req.Messages {
		if m.Role == domain.RoleUser && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

func invoiceField(text, label string) string {
	for line := range strings.SplitSeq(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), label) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func looksLikeInvoice(text string) bool {
	return invoiceField(text, "Total") != "" && invoiceField(text, "Vendor") != ""
}

func parseAmount(s string) float64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", ""), 64)
	if err != nil {
		return 0
	}
	return v
}

func extract(text string, p profile, caseID, sample string) domain.Response {
	total := parseAmount(invoiceField(text, "Total"))
	ok := unit("extract", caseID, sample) < caseProbability(p.accuracy, caseID)
	if !ok {
		total = math.Round(total) / 10
	}
	out := map[string]any{
		"vendor": map[string]any{
			"name":   invoiceField(text, "Vendor"),
			"vat_id": invoiceField(text, "VAT ID"),
		},
		"total":    total,
		"currency": invoiceField(text, "Currency"),
		"date":     invoiceField(text, "Date"),
	}
	b, err := json.Marshal(out)
	if err != nil {
		return domain.Response{Text: "{}", StopReason: domain.StopEnd}
	}
	return domain.Response{Text: string(b), StopReason: domain.StopEnd}
}

func hasTool(req domain.Request, name string) bool {
	for _, t := range req.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

type toolEvent struct {
	name    string
	content string
	isError bool
}

func history(req domain.Request) []toolEvent {
	var out []toolEvent
	for _, m := range req.Messages {
		for _, r := range m.ToolResults {
			out = append(out, toolEvent{name: r.Name, content: r.Content, isError: r.IsError})
		}
	}
	return out
}

func call(id, name string, args map[string]any) domain.Response {
	b, err := json.Marshal(args)
	if err != nil {
		b = []byte("{}")
	}
	return domain.Response{
		ToolCalls:  []domain.ToolCall{{ID: id, Name: name, Arguments: b}},
		StopReason: domain.StopToolUse,
	}
}

func final(text string) domain.Response {
	return domain.Response{Text: text, StopReason: domain.StopEnd}
}

func (e *Engine) agentStep(req domain.Request, p profile, caseID, sample string) domain.Response {
	order := e.order.FindString(firstUser(req))
	events := history(req)
	step := strconv.Itoa(len(events))
	id := "call_" + step
	if order == "" || !hasTool(req, "lookup_order") {
		return final("Could you share your order number?")
	}
	if len(events) == 0 {
		return call(id, "lookup_order", map[string]any{"order_id": order})
	}
	last := events[len(events)-1]
	switch last.name {
	case "lookup_order":
		if last.isError || strings.Contains(last.content, "not_found") {
			if unit("wrong_refund", caseID, sample) < p.wrongRefund && hasTool(req, "issue_refund") {
				return call(id, "issue_refund", map[string]any{"order_id": order, "reason": "customer request"})
			}
			return final(fmt.Sprintf("I'm sorry, I could not find order %s. Please double-check the number.", order))
		}
		if unit("skip_policy", caseID, sample) >= p.skipPolicy && hasTool(req, "check_refund_policy") {
			return call(id, "check_refund_policy", map[string]any{"order_id": order})
		}
		return call(id, "issue_refund", map[string]any{"order_id": order, "reason": "damaged"})
	case "check_refund_policy":
		if strings.Contains(last.content, `"eligible":false`) {
			return final(fmt.Sprintf("Order %s is not eligible for a refund under the current policy.", order))
		}
		return call(id, "issue_refund", map[string]any{"order_id": order, "reason": "damaged"})
	case "issue_refund":
		ref := "the refund"
		var m map[string]any
		if json.Unmarshal([]byte(last.content), &m) == nil {
			if r, ok := m["refund_id"].(string); ok {
				ref = "reference " + r
			}
		}
		return final(fmt.Sprintf("Your refund for order %s has been queued (%s).", order, ref))
	default:
		return final("Done.")
	}
}

type criterion struct {
	ID   string
	Text string
}

func parseJudgePrompt(text string) (string, []criterion) {
	output := ""
	if _, rest, ok := strings.Cut(text, "<output>"); ok {
		output, _, _ = strings.Cut(rest, "</output>")
	}
	var crit []criterion
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		id, rest, ok := strings.Cut(strings.TrimPrefix(line, "- ["), "]")
		if ok {
			crit = append(crit, criterion{ID: id, Text: strings.TrimSpace(rest)})
		}
	}
	return output, crit
}

func (e *Engine) judge(req domain.Request) domain.Response {
	output, crit := parseJudgePrompt(lastUser(req))
	type verdict struct {
		ID       string `json:"id"`
		Pass     bool   `json:"pass"`
		Evidence string `json:"evidence"`
	}
	res := struct {
		Criteria []verdict `json:"criteria"`
		Score    float64   `json:"score"`
	}{}
	passed := 0
	for _, c := range crit {
		tokens := e.token.FindAllString(c.Text, -1)
		pass := strings.TrimSpace(output) != ""
		evidence := "output present"
		for _, t := range tokens {
			t = strings.Trim(t, `"`)
			if !strings.Contains(strings.ToLower(output), strings.ToLower(t)) {
				pass = false
				evidence = "missing " + t
				break
			}
			evidence = "found " + t
		}
		if pass {
			passed++
		}
		res.Criteria = append(res.Criteria, verdict{ID: c.ID, Pass: pass, Evidence: evidence})
	}
	if len(crit) > 0 {
		res.Score = float64(passed) / float64(len(crit))
	}
	b, err := json.Marshal(res)
	if err != nil {
		return final("{}")
	}
	return final(string(b))
}
