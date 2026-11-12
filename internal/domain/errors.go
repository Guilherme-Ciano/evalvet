package domain

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	ErrBudgetExceeded = errors.New("budget exceeded")
	ErrCacheMiss      = errors.New("cache miss in replay mode")
	ErrConfig         = errors.New("invalid configuration")
	ErrProvider       = errors.New("provider error")
	ErrAuth           = errors.New("provider authentication failed")
)

type ProviderError struct {
	Provider   string
	StatusCode int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.Provider, e.StatusCode, e.Message)
}

func (e *ProviderError) Is(target error) bool {
	if target == ErrProvider {
		return true
	}
	return target == ErrAuth && (e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden)
}
