package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Guilherme-Ciano/evalvet/internal/baseline"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/transport"
)

const (
	ExitOK          = 0
	ExitRegression  = 1
	ExitUsage       = 2
	ExitInfra       = 3
	ExitBudget      = 4
	ExitInterrupted = 130
)

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if ee, ok := errors.AsType[*ExitError](err); ok {
		return ee.Code
	}
	switch {
	case errors.Is(err, domain.ErrBudgetExceeded):
		return ExitBudget
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.Is(err, domain.ErrConfig), errors.Is(err, baseline.ErrNotFound):
		return ExitUsage
	case errors.Is(err, domain.ErrAuth),
		errors.Is(err, domain.ErrCacheMiss),
		errors.Is(err, transport.ErrReplayMiss),
		errors.Is(err, domain.ErrProvider):
		return ExitInfra
	default:
		return ExitInfra
	}
}

func usageError(format string, args ...any) error {
	return &ExitError{Code: ExitUsage, Err: fmt.Errorf(format, args...)}
}

const (
	formatTable = "table"
	formatJSON  = "json"
)
