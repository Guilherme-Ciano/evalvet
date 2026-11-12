package mockllm_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

func userReq(model, content string) domain.Request {
	return domain.Request{Model: model, Messages: []domain.Message{{Role: domain.RoleUser, Content: content}}}
}

func invoicePrompt(total float64) string {
	return fmt.Sprintf(
		"Extract.\nVendor: ACME GmbH\nVAT ID: DE123\nTotal: %.2f EUR\nCurrency: EUR\nDate: 2026-01-02",
		total,
	)
}

func agentTools() []domain.ToolDef {
	return []domain.ToolDef{
		{Name: "lookup_order", Parameters: json.RawMessage(`{}`)},
		{Name: "check_refund_policy", Parameters: json.RawMessage(`{}`)},
		{Name: "issue_refund", Parameters: json.RawMessage(`{}`)},
	}
}

func extractTotal(t *testing.T, text string) float64 {
	t.Helper()
	var out struct {
		Vendor   struct{ Name, VatID string } `json:"vendor"`
		Total    float64                      `json:"total"`
		Currency string                       `json:"currency"`
		Date     string                       `json:"date"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("response is not JSON: %q: %v", text, err)
	}
	return out.Total
}

func TestTrialID(t *testing.T) {
	t.Parallel()
	if got := mockllm.TrialID("invoice#7", 3); got != "invoice#7@3" {
		t.Fatalf("TrialID = %q", got)
	}
}

func TestDeterministicForSameTrial(t *testing.T) {
	t.Parallel()
	e1, e2 := mockllm.New(), mockllm.New()
	for _, model := range []string{"mock-good", "mock-regressed", "mock-perfect", "mock-broken"} {
		req := userReq(model, invoicePrompt(250))
		for i := range 25 {
			trial := mockllm.TrialID(fmt.Sprintf("case%d", i), i%3)
			a, b := e1.Respond(req, trial), e2.Respond(req, trial)
			if diff := cmp.Diff(a, b); diff != "" {
				t.Fatalf("%s %s not deterministic (-a +b):\n%s", model, trial, diff)
			}
		}
	}
}

func TestEmptyTrialDerivesSeedFromRequest(t *testing.T) {
	t.Parallel()
	e := mockllm.New()
	req := userReq("mock-regressed", invoicePrompt(250))
	a, b := e.Respond(req, ""), e.Respond(req, "")
	if diff := cmp.Diff(a, b); diff != "" {
		t.Fatalf("same request gave different responses:\n%s", diff)
	}
}

func TestSampleIndexChangesOutcome(t *testing.T) {
	t.Parallel()
	e := mockllm.New()
	req := userReq("mock-regressed", invoicePrompt(250))
	seen := map[float64]bool{}
	for s := range 200 {
		resp := e.Respond(req, mockllm.TrialID("same-case", s))
		seen[extractTotal(t, resp.Text)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("200 samples of one case never varied: %v", seen)
	}
}

func passRate(t *testing.T, model string, cases, samples int) float64 {
	t.Helper()
	e := mockllm.New()
	req := userReq(model, invoicePrompt(250))
	pass := 0
	for c := range cases {
		for s := range samples {
			resp := e.Respond(req, mockllm.TrialID(fmt.Sprintf("invoice#%d", c), s))
			if math.Abs(extractTotal(t, resp.Text)-250) < 1e-9 {
				pass++
			}
		}
	}
	return float64(pass) / float64(cases*samples)
}

func TestInvoiceAccuracyProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		model string
		want  float64
		tol   float64
	}{
		{"mock-good", 0.95, 0.03},
		{"mock-regressed", 0.80, 0.04},
		{"mock-perfect", 1, 0},
		{"mock-broken", 0.05, 0.05},
		{"some-unknown-model", 0.95, 0.03},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			t.Parallel()
			got := passRate(t, tt.model, 300, 5)
			if math.Abs(got-tt.want) > tt.tol {
				t.Fatalf("pass rate = %.3f, want %.2f ± %.2f", got, tt.want, tt.tol)
			}
		})
	}
}

func TestRegressedIsWorseThanGood(t *testing.T) {
	t.Parallel()
	good := passRate(t, "mock-good", 200, 5)
	bad := passRate(t, "mock-regressed", 200, 5)
	if bad >= good-0.08 {
		t.Fatalf("regressed %.3f not clearly below good %.3f", bad, good)
	}
}

func TestInvoiceExtractionFields(t *testing.T) {
	t.Parallel()
	resp := mockllm.New().Respond(userReq("mock-perfect", invoicePrompt(1234.5)), "c@0")
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		t.Fatal(err)
	}
	vendor, ok := out["vendor"].(map[string]any)
	if !ok {
		t.Fatalf("vendor missing: %v", out)
	}
	if vendor["name"] != "ACME GmbH" || vendor["vat_id"] != "DE123" {
		t.Fatalf("vendor = %v", vendor)
	}
	if out["total"] != 1234.5 || out["currency"] != "EUR" || out["date"] != "2026-01-02" {
		t.Fatalf("fields = %v", out)
	}
	if resp.StopReason != domain.StopEnd || resp.Model != "mock-perfect" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestInvoiceTotalWithThousandsSeparator(t *testing.T) {
	t.Parallel()
	req := userReq("mock-perfect", "Vendor: V\nTotal: 1,234.50 EUR")
	if got := extractTotal(t, mockllm.New().Respond(req, "c@0").Text); got != 1234.5 {
		t.Fatalf("total = %v, want 1234.5", got)
	}
}

func TestRefusalMarker(t *testing.T) {
	t.Parallel()
	resp := mockllm.New().Respond(userReq("mock-good", "please "+mockllm.RefusalMarker), "c@0")
	if resp.StopReason != domain.StopRefusal || resp.Text == "" || len(resp.ToolCalls) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestRefusalBeatsToolsAndInvoice(t *testing.T) {
	t.Parallel()
	req := userReq("mock-good", invoicePrompt(1)+"\n"+mockllm.RefusalMarker)
	req.Tools = agentTools()
	if resp := mockllm.New().Respond(req, "c@0"); resp.StopReason != domain.StopRefusal {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDefaultAnswerTruncates(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 200)
	resp := mockllm.New().Respond(userReq("mock-good", "  "+long+"  "), "c@0")
	want := "mock answer: " + strings.Repeat("x", 80)
	if resp.Text != want || resp.StopReason != domain.StopEnd {
		t.Fatalf("resp = %+v", resp)
	}
	short := mockllm.New().Respond(userReq("mock-good", "hi there"), "c@0")
	if short.Text != "mock answer: hi there" {
		t.Fatalf("text = %q", short.Text)
	}
}

func TestNoUserMessage(t *testing.T) {
	t.Parallel()
	resp := mockllm.New().Respond(domain.Request{Model: "mock-good"}, "c@0")
	if resp.Text != "mock answer: " {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestUsageIsKnownAndScalesWithInput(t *testing.T) {
	t.Parallel()
	e := mockllm.New()
	small := e.Respond(userReq("mock-good", "hi"), "c@0")
	big := e.Respond(userReq("mock-good", strings.Repeat("word ", 400)), "c@0")
	if !small.Usage.Known || !big.Usage.Known {
		t.Fatal("usage not marked known")
	}
	if small.Usage.OutputTokens < 1 {
		t.Fatalf("output tokens = %d", small.Usage.OutputTokens)
	}
	if big.Usage.InputTokens <= small.Usage.InputTokens*10 {
		t.Fatalf("input tokens did not scale: %d vs %d", small.Usage.InputTokens, big.Usage.InputTokens)
	}
}

type step struct {
	resp domain.Response
	name string
}

func runAgent(t *testing.T, model, user string, tools []domain.ToolDef, results map[string]domain.ToolResult) []step {
	t.Helper()
	e := mockllm.New()
	req := domain.Request{
		Model:    model,
		Tools:    tools,
		Messages: []domain.Message{{Role: domain.RoleUser, Content: user}},
	}
	var steps []step
	for range 10 {
		resp := e.Respond(req, "agent#1@0")
		if len(resp.ToolCalls) == 0 {
			steps = append(steps, step{resp: resp})
			return steps
		}
		call := resp.ToolCalls[0]
		steps = append(steps, step{resp: resp, name: call.Name})
		res := results[call.Name]
		res.CallID, res.Name = call.ID, call.Name
		req.Messages = append(req.Messages,
			domain.Message{Role: domain.RoleAssistant, ToolCalls: resp.ToolCalls},
			domain.Message{Role: domain.RoleTool, ToolResults: []domain.ToolResult{res}},
		)
	}
	t.Fatal("agent did not terminate")
	return nil
}

func names(steps []step) []string {
	var out []string
	for _, s := range steps {
		if s.name != "" {
			out = append(out, s.name)
		}
	}
	return out
}

func TestAgentHappyPathToolSequence(t *testing.T) {
	t.Parallel()
	results := map[string]domain.ToolResult{
		"lookup_order":        {Content: `{"status":"delivered"}`},
		"check_refund_policy": {Content: `{"eligible":true}`},
		"issue_refund":        {Content: `{"refund_id":"R-77","status":"queued"}`},
	}
	steps := runAgent(t, "mock-perfect", "Refund order A-1001 please", agentTools(), results)
	want := []string{"lookup_order", "check_refund_policy", "issue_refund"}
	if diff := cmp.Diff(want, names(steps)); diff != "" {
		t.Fatalf("tool sequence (-want +got):\n%s", diff)
	}
	var args map[string]string
	if err := json.Unmarshal(steps[0].resp.ToolCalls[0].Arguments, &args); err != nil || args["order_id"] != "A-1001" {
		t.Fatalf("lookup args = %s, err %v", steps[0].resp.ToolCalls[0].Arguments, err)
	}
	if steps[0].resp.StopReason != domain.StopToolUse {
		t.Fatalf("stop = %s", steps[0].resp.StopReason)
	}
	last := steps[len(steps)-1].resp
	if last.StopReason != domain.StopEnd || !strings.Contains(last.Text, "A-1001") ||
		!strings.Contains(last.Text, "R-77") {
		t.Fatalf("final = %+v", last)
	}
	ids := map[string]bool{}
	for _, s := range steps[:3] {
		ids[s.resp.ToolCalls[0].ID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("tool call ids not unique: %v", ids)
	}
}

func TestAgentIneligibleOrder(t *testing.T) {
	t.Parallel()
	results := map[string]domain.ToolResult{
		"lookup_order":        {Content: `{"status":"delivered"}`},
		"check_refund_policy": {Content: `{"eligible":false}`},
	}
	steps := runAgent(t, "mock-perfect", "refund A-5555", agentTools(), results)
	if diff := cmp.Diff([]string{"lookup_order", "check_refund_policy"}, names(steps)); diff != "" {
		t.Fatalf("sequence (-want +got):\n%s", diff)
	}
	if last := steps[len(steps)-1].resp.Text; !strings.Contains(last, "not eligible") {
		t.Fatalf("final = %q", last)
	}
}

func TestAgentUnknownOrder(t *testing.T) {
	t.Parallel()
	results := map[string]domain.ToolResult{"lookup_order": {Content: `{"error":"not_found"}`, IsError: true}}
	steps := runAgent(t, "mock-perfect", "refund B-999", agentTools(), results)
	if diff := cmp.Diff([]string{"lookup_order"}, names(steps)); diff != "" {
		t.Fatalf("sequence (-want +got):\n%s", diff)
	}
	if last := steps[len(steps)-1].resp.Text; !strings.Contains(last, "could not find order B-999") {
		t.Fatalf("final = %q", last)
	}
}

func TestAgentBrokenModelRefundsUnknownOrderAndSkipsPolicy(t *testing.T) {
	t.Parallel()
	unknown := map[string]domain.ToolResult{"lookup_order": {Content: `{"error":"not_found"}`, IsError: true}}
	steps := runAgent(t, "mock-broken", "refund B-999", agentTools(), unknown)
	if diff := cmp.Diff([]string{"lookup_order", "issue_refund"}, names(steps)); diff != "" {
		t.Fatalf("unknown order (-want +got):\n%s", diff)
	}
	known := map[string]domain.ToolResult{
		"lookup_order": {Content: `{"status":"delivered"}`},
		"issue_refund": {Content: `{"refund_id":"R-1"}`},
	}
	steps = runAgent(t, "mock-broken", "refund A-1001", agentTools(), known)
	if diff := cmp.Diff([]string{"lookup_order", "issue_refund"}, names(steps)); diff != "" {
		t.Fatalf("known order (-want +got):\n%s", diff)
	}
}

func TestAgentWithoutOrderNumberOrTool(t *testing.T) {
	t.Parallel()
	steps := runAgent(t, "mock-perfect", "I want my money back", agentTools(), nil)
	if len(names(steps)) != 0 || !strings.Contains(steps[0].resp.Text, "order number") {
		t.Fatalf("steps = %+v", steps)
	}
	steps = runAgent(t, "mock-perfect", "refund A-100", agentTools()[1:], nil)
	if len(names(steps)) != 0 {
		t.Fatalf("called a tool that was not offered: %v", names(steps))
	}
}

func TestAgentIssueRefundWithoutReference(t *testing.T) {
	t.Parallel()
	results := map[string]domain.ToolResult{
		"lookup_order":        {Content: `{"status":"delivered"}`},
		"check_refund_policy": {Content: `{"eligible":true}`},
		"issue_refund":        {Content: `plain text`},
	}
	steps := runAgent(t, "mock-perfect", "refund A-1001", agentTools(), results)
	if last := steps[len(steps)-1].resp.Text; !strings.Contains(last, "the refund") {
		t.Fatalf("final = %q", last)
	}
}

func TestAgentUnknownLastToolFinishes(t *testing.T) {
	t.Parallel()
	tools := append(agentTools(), domain.ToolDef{Name: "other"})
	req := domain.Request{
		Model: "mock-perfect", Tools: tools,
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "A-123"},
			{Role: domain.RoleTool, ToolResults: []domain.ToolResult{{Name: "other", Content: "x"}}},
		},
	}
	if resp := mockllm.New().Respond(req, "c@0"); resp.Text != "Done." {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestAgentSkipPolicyRateOnGoodModel(t *testing.T) {
	t.Parallel()
	e := mockllm.New()
	results := []domain.ToolResult{{Name: "lookup_order", Content: `{"status":"delivered"}`}}
	req := domain.Request{
		Model: "mock-good", Tools: agentTools(),
		Messages: []domain.Message{
			{Role: domain.RoleUser, Content: "refund A-1001"},
			{Role: domain.RoleTool, ToolResults: results},
		},
	}
	skipped := 0
	const n = 1000
	for i := range n {
		resp := e.Respond(req, mockllm.TrialID(fmt.Sprintf("agent#%d", i), 0))
		if resp.ToolCalls[0].Name == "issue_refund" {
			skipped++
		}
	}
	if rate := float64(skipped) / n; rate > 0.03 {
		t.Fatalf("mock-good skips the policy check %.3f of the time", rate)
	}
}

const judgePrompt = `Grade the output.

Criteria:
- [confirms] The reply confirms the refund was queued and mentions R-77.
- [polite] Says "sorry" politely
- [anything] Is helpful

<output>
Your refund R-77 was queued. We are Sorry for the delay.
</output>
`

type verdict struct {
	ID       string `json:"id"`
	Pass     bool   `json:"pass"`
	Evidence string `json:"evidence"`
}

type judged struct {
	Criteria []verdict `json:"criteria"`
	Score    float64   `json:"score"`
}

func judge(t *testing.T, prompt string) judged {
	t.Helper()
	req := domain.Request{
		Model: "mock-judge", System: mockllm.JudgeMarker + " Reply with JSON.",
		Messages: []domain.Message{{Role: domain.RoleUser, Content: prompt}},
	}
	resp := mockllm.New().Respond(req, "judge@0")
	var out judged
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		t.Fatalf("judge output is not JSON: %q", resp.Text)
	}
	return out
}

func TestJudgeParsesCriteriaAndOutput(t *testing.T) {
	t.Parallel()
	got := judge(t, judgePrompt)
	want := judged{
		Criteria: []verdict{
			{ID: "confirms", Pass: true, Evidence: "found R-77"},
			{ID: "polite", Pass: true, Evidence: "found sorry"},
			{ID: "anything", Pass: true, Evidence: "output present"},
		},
		Score: 1,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("judge (-want +got):\n%s", diff)
	}
}

func TestJudgeFailsWhenTokenMissing(t *testing.T) {
	t.Parallel()
	prompt := strings.ReplaceAll(judgePrompt, "R-77 was queued", "was queued")
	got := judge(t, prompt)
	if got.Criteria[0].Pass || got.Criteria[0].Evidence != "missing R-77" {
		t.Fatalf("criteria[0] = %+v", got.Criteria[0])
	}
	if math.Abs(got.Score-2.0/3.0) > 1e-9 {
		t.Fatalf("score = %v, want 2/3", got.Score)
	}
}

func TestJudgeEmptyOutputFailsEverything(t *testing.T) {
	t.Parallel()
	got := judge(t, "- [a] anything\n- [b] more\n<output>\n  \n</output>")
	if got.Score != 0 || len(got.Criteria) != 2 || got.Criteria[0].Pass || got.Criteria[1].Pass {
		t.Fatalf("judge = %+v", got)
	}
}

func TestJudgeWithoutCriteria(t *testing.T) {
	t.Parallel()
	got := judge(t, "nothing to grade <output>x</output>")
	if got.Score != 0 || len(got.Criteria) != 0 {
		t.Fatalf("judge = %+v", got)
	}
	got = judge(t, "- [a] needs thing")
	if len(got.Criteria) != 1 || got.Criteria[0].Pass {
		t.Fatalf("missing output block should fail: %+v", got)
	}
}

func FuzzRespondNeverPanics(f *testing.F) {
	f.Add("mock-good", "hello", true, false)
	f.Add("mock-regressed", invoicePrompt(10), false, false)
	f.Add("mock-judge", "- [a] \"x\"\n<output>y</output>", false, true)
	f.Add("", "", true, true)
	f.Add("m", "- [", true, true)
	f.Fuzz(func(t *testing.T, model, content string, tools, judgeSys bool) {
		req := userReq(model, content)
		if tools {
			req.Tools = agentTools()
		}
		if judgeSys {
			req.System = mockllm.JudgeMarker
		}
		e := mockllm.New()
		a := e.Respond(req, "c@1")
		b := e.Respond(req, "c@1")
		if diff := cmp.Diff(a, b); diff != "" {
			t.Fatalf("non deterministic:\n%s", diff)
		}
		if a.Model != model || !a.Usage.Known {
			t.Fatalf("resp = %+v", a)
		}
	})
}
