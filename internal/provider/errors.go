package provider

import (
	"net/http"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

func HTTPError(name string, status int, header http.Header, msg string) *domain.ProviderError {
	return &domain.ProviderError{
		Provider:   name,
		StatusCode: status,
		Message:    msg,
		Retryable:  transport.Retryable(status),
		RetryAfter: transport.ParseRetryAfter(header, time.Now()),
	}
}
