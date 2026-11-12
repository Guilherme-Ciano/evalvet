package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := Execute(ctx, os.Args[1:], Streams{Out: os.Stdout, Err: os.Stderr}, os.LookupEnv, BuildInfo{Version: version, Commit: commit, Date: date})
	stop()
	os.Exit(code)
}
