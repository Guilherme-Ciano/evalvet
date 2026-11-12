package format_test

import (
	"testing"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/report/format"
)

func TestDurRoundsUpToThousandMilliseconds(t *testing.T) {
	t.Parallel()
	if got := format.Dur(999600 * time.Microsecond); got == "1000ms" {
		t.Fatalf("Dur(999.6ms) = %q, want 1.0s", got)
	}
}
