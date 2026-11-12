package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
)

type Streams struct {
	Out io.Writer
	Err io.Writer
}

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

type cli struct {
	streams   Streams
	env       config.LookupEnv
	build     BuildInfo
	logFormat string
	verbose   bool
}

func (c *cli) getenv(k string) string {
	v, _ := c.env(k)
	return v
}

func (c *cli) logger() *slog.Logger {
	lvl := slog.LevelWarn
	if c.verbose {
		lvl = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if c.logFormat == "json" {
		return slog.New(slog.NewJSONHandler(c.streams.Err, opts))
	}
	return slog.New(slog.NewTextHandler(c.streams.Err, opts))
}

func newRoot(c *cli) *cobra.Command {
	root := &cobra.Command{
		Use:           "evalvet",
		Short:         "Statistical regression tests for prompts and agents",
		Long:          "evalvet samples every case k times and gates CI on a paired permutation test and bootstrap CI against a baseline, instead of a fixed threshold.",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetOut(c.streams.Out)
	root.SetErr(c.streams.Err)
	root.PersistentFlags().StringVar(&c.logFormat, "log-format", "text", "log format: text or json")
	root.PersistentFlags().BoolVarP(&c.verbose, "verbose", "v", false, "debug logging")
	root.AddCommand(
		newRunCmd(c), newValidateCmd(c), newCompareCmd(c), newBaselineCmd(c), newReportCmd(c),
		newInitCmd(c), newPricingCmd(c), newVersionCmd(c),
	)
	return root
}

func Execute(ctx context.Context, args []string, streams Streams, env config.LookupEnv, build BuildInfo) int {
	c := &cli{streams: streams, env: env, build: build}
	root := newRoot(c)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code := exitCode(err)
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Err != nil {
		fmt.Fprintln(streams.Err, "evalvet:", err)
	}
	if code == ExitUsage && !isConfigError(err) {
		fmt.Fprintln(streams.Err, "run 'evalvet --help' for usage")
	}
	return code
}

func isConfigError(err error) bool {
	_, ok := config.AsValidation(err)
	return ok
}
