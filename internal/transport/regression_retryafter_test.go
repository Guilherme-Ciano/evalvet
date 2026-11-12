package transport_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

func TestParseRetryAfterHugeValueOverflowsToNegative(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"1e30", "Inf", "9999999999999"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			got := transport.ParseRetryAfter(http.Header{"Retry-After": {v}}, time.Now())
			if got < 0 {
				t.Fatalf("ParseRetryAfter(%q) = %v, want non-negative", v, got)
			}
		})
	}
}
