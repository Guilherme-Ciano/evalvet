package main

import (
	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/app"
	"github.com/Guilherme-Ciano/evalvet/internal/baseline"
	"github.com/Guilherme-Ciano/evalvet/internal/compare"
	"github.com/Guilherme-Ciano/evalvet/internal/report/table"
)

func newCompareCmd(c *cli) *cobra.Command {
	var (
		in, base, mode string
		margin         float64
	)
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare a results.json against a baseline without re-running",
		RunE: func(_ *cobra.Command, _ []string) error {
			if base == "" {
				return usageError("--baseline is required")
			}
			if mode != compare.ModeRegression && mode != compare.ModeNonInferiority {
				return usageError("--mode must be regression or noninferiority")
			}
			results, err := readResults(in)
			if err != nil {
				return err
			}
			b, err := baseline.Load(base)
			if err != nil {
				return err
			}
			failing := false
			for _, r := range results {
				m := r.Method
				r.Comparisons = app.CompareBaseline(b, r, compare.Options{
					Alpha: m.Alpha, MinEffect: m.MinEffect, Mode: mode, Margin: margin,
					Seed: m.Seed, Permutations: m.Permutations, Bootstrap: m.Bootstrap,
				})
				r.Gates = nil
				if err := table.Write(c.streams.Out, r); err != nil {
					return err
				}
				failing = failing || r.Failing()
			}
			if failing {
				return &ExitError{Code: ExitRegression}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&in, "in", ".evalvet/out/results.json", "results.json (or directory)")
	cmd.Flags().StringVar(&base, "baseline", "", "baseline file")
	cmd.Flags().StringVar(&mode, "mode", compare.ModeRegression, "regression or noninferiority")
	cmd.Flags().Float64Var(&margin, "margin", 0.03, "non-inferiority margin")
	return cmd
}
