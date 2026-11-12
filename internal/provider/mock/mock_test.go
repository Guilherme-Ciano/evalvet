package mock_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
	"github.com/Guilherme-Ciano/evalvet/internal/provider/mock"
)

func req(model, text, trial string) domain.Request {
	return domain.Request{
		Model: model, TrialID: trial,
		Messages: []domain.Message{{Role: domain.RoleUser, Content: text}},
	}
}

func TestIdentityAndCapabilities(t *testing.T) {
	t.Parallel()
	p := mock.New("m1", 0)
	if p.Name() != "m1" || p.Kind() != "mock" {
		t.Fatalf("name %q kind %q", p.Name(), p.Kind())
	}
	caps := p.Capabilities()
	if !caps.Temperature || !caps.Seed || !caps.Tools || caps.Streaming {
		t.Fatalf("caps = %+v", caps)
	}
	if got := domain.CapabilitiesOf(p); got != caps {
		t.Fatalf("CapabilitiesOf = %+v", got)
	}
}

func TestCompleteMatchesEngine(t *testing.T) {
	t.Parallel()
	p := mock.New("m", 0)
	r := req("mock-regressed", "Vendor: A\nTotal: 250 EUR", "inv#1@0")
	got, err := p.Complete(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	want := mockllm.New().Respond(r, r.TrialID)
	got.Timing = domain.Timing{}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("response differs from engine (-want +got):\n%s", diff)
	}
}

func TestTimingInvariants(t *testing.T) {
	t.Parallel()
	p := mock.New("m", 3*time.Millisecond)
	resp, err := p.Complete(t.Context(), req("mock-good", "hi", "c@0"))
	if err != nil {
		t.Fatal(err)
	}
	tm := resp.Timing
	if tm.Total < 3*time.Millisecond {
		t.Fatalf("Total %v shorter than the configured latency", tm.Total)
	}
	if tm.TTFB != tm.Total || tm.FirstToken != tm.Total || tm.Start.IsZero() {
		t.Fatalf("timing = %+v", tm)
	}
}

func TestDeterministicAcrossCalls(t *testing.T) {
	t.Parallel()
	p := mock.New("m", 0)
	r := req("mock-regressed", "Vendor: A\nTotal: 250 EUR", "inv#4@1")
	a, err := p.Complete(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Complete(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != b.Text || a.Usage != b.Usage {
		t.Fatalf("%+v != %+v", a, b)
	}
}

func TestCanceledContext(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		latency time.Duration
	}{
		{"before the call without latency", 0},
		{"before the call with latency", time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := mock.New("m", tt.latency).Complete(ctx, req("mock-good", "hi", "c@0"))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			if !strings.Contains(err.Error(), "mock m") {
				t.Fatalf("err lacks provider name: %v", err)
			}
		})
	}
}

func TestDeadlineDuringLatency(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err := mock.New("m", time.Hour).Complete(ctx, req("mock-good", "hi", "c@0"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestToolCallsFromEngine(t *testing.T) {
	t.Parallel()
	r := req("mock-perfect", "refund A-1001", "agent#1@0")
	r.Tools = []domain.ToolDef{{Name: "lookup_order"}}
	resp, err := mock.New("m", 0).Complete(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != domain.StopToolUse || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup_order" {
		t.Fatalf("resp = %+v", resp)
	}
}
