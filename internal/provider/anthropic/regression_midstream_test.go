package anthropic_test

import (
	"errors"
	"testing"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func TestMidStreamOverloadedErrorIsRetryable(t *testing.T) {
	t.Parallel()
	_, err := completeFixture(t, "mid_stream_error.sse", 200, nil)
	var pe *domain.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
	if !pe.Retryable {
		t.Fatalf("overloaded_error event reported as non-retryable with status %d", pe.StatusCode)
	}
}
