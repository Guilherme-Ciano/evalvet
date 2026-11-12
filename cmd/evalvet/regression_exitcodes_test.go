package main

import "testing"

func TestUsageErrorsExitWith2(t *testing.T) {
	t.Parallel()
	tiny := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"run", tiny, "--no-such-flag"}},
		{"unknown command", []string{"frobnicate"}},
		{"run without suite", []string{"run"}},
		{"validate without suite", []string{"validate"}},
		{"unknown output format", []string{"run", tiny, "--cache", "off", "--format", "xml"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := exec(t, nil, tt.args...)
			if r.code != ExitUsage {
				t.Fatalf("exit = %d, want %d; stderr: %s", r.code, ExitUsage, r.err)
			}
		})
	}
}

func TestAbortedRunDoesNotPrintPass(t *testing.T) {
	t.Parallel()
	tiny := suiteFile(t, "tiny.eval.yaml", tinyMockSuite)
	r := exec(t, nil, "run", tiny, "--cache", "off", "--budget-usd", "0.0000001")
	if r.code != ExitBudget {
		t.Fatalf("exit = %d", r.code)
	}
	if r.out != "" && containsPass(r.out) {
		t.Fatalf("budget-aborted run printed a PASS verdict:\n%s", r.out)
	}
}

func containsPass(s string) bool {
	for i := 0; i+len("result: PASS") <= len(s); i++ {
		if s[i:i+len("result: PASS")] == "result: PASS" {
			return true
		}
	}
	return false
}
