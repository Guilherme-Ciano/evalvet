package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/mockllm"
)

func run(ctx context.Context, bin, suite, model string) (int, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 2, fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{
		Handler:           mockllm.NewServer(mockllm.ServerOptions{Latency: 20 * time.Millisecond, Jitter: 10 * time.Millisecond}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	cmd := exec.CommandContext(ctx, bin, "run", suite, "--cache", "off")
	cmd.Env = append(os.Environ(), "MOCK_URL=http://"+ln.Addr().String(), "DEMO_MODEL="+model)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 2, fmt.Errorf("run evalvet: %w", err)
	}
	return 0, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	model := os.Getenv("DEMO_MODEL")
	if model == "" {
		model = "mock-regressed"
	}
	code, err := run(ctx, "bin/evalvet", "examples/demo/demo.eval.yaml", model)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Fprintf(os.Stderr, "evalvet exit code: %d\n", code)
	os.Exit(code)
}
