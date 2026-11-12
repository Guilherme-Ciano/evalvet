package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

func healthcheck(url string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("mockprovider", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	latency := fs.Duration("latency", 0, "mean response latency")
	jitter := fs.Duration("jitter", 0, "uniform latency jitter (±)")
	chunkDelay := fs.Duration("chunk-delay", 0, "delay between SSE chunks")
	every := fs.Int64("rate-limit-every", 0, "answer every Nth request with 429 and retry-after")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("flags: %w", err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mockllm.NewServer(mockllm.ServerOptions{Latency: *latency, Jitter: *jitter, ChunkDelay: *chunkDelay, RateLimitEvery: *every}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "mockprovider listening", slog.String("addr", *addr), slog.Duration("latency", *latency))
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		url := "http://127.0.0.1:8080/healthz"
		if len(os.Args) > 2 {
			url = os.Args[2]
		}
		os.Exit(healthcheck(url))
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], log)
	stop()
	if err != nil {
		log.Error("mockprovider failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
