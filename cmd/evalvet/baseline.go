package main

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/baseline"
	"github.com/Guilherme-Ciano/evalvet/internal/report/format"
)

func newBaselineCmd(c *cli) *cobra.Command {
	cmd := &cobra.Command{Use: "baseline", Short: "Save or show the committed baseline"}
	var in, out string
	save := &cobra.Command{
		Use:   "save",
		Short: "Turn a results.json into a baseline file",
		RunE: func(_ *cobra.Command, _ []string) error {
			results, err := readResults(in)
			if err != nil {
				return err
			}
			if len(results) != 1 {
				return usageError("baseline save needs exactly one results.json, found %d", len(results))
			}
			path := out
			if path == "" {
				path = ".evalvet/baselines/" + results[0].Suite + ".json"
			}
			if err := baseline.Save(
				path,
				baseline.FromRun(results[0], c.getenv("GITHUB_SHA"), time.Now()),
			); err != nil {
				return err
			}
			fmt.Fprintf(c.streams.Out, "baseline for %s written to %s\n", results[0].Suite, path)
			return nil
		},
	}
	save.Flags().StringVar(&in, "in", ".evalvet/out/results.json", "results.json to save")
	save.Flags().StringVar(&out, "out", "", "baseline path (default .evalvet/baselines/<suite>.json)")
	show := &cobra.Command{
		Use:   "show <baseline.json>",
		Short: "Print a baseline",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			b, err := baseline.Load(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(c.streams.Out, "suite %s · created %s · git %s · config %s · pricing %s\n\n",
				b.Suite, b.CreatedAt.Format(time.RFC3339), b.GitSHA, short(b.ConfigHash), b.PricingVersion)
			tw := tabwriter.NewWriter(c.streams.Out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "TARGET\tMODEL\tPASS RATE\tCASES\tTRIALS\tCOST\tP95")
			for _, t := range b.Targets {
				fmt.Fprintf(
					tw,
					"%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
					t.Target,
					t.Model,
					format.Rate(t.PassRate),
					len(t.Cases),
					t.Trials,
					format.Cost(t.CostUSD),
					format.Dur(t.LatencyP95),
				)
			}
			if err := tw.Flush(); err != nil {
				return fmt.Errorf("print baseline: %w", err)
			}
			return nil
		},
	}
	cmd.AddCommand(save, show)
	return cmd
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
