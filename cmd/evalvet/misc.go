package main

import (
	"fmt"
	"runtime"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/app"
	"github.com/Guilherme-Ciano/evalvet/internal/pricing"
)

func newVersionCmd(c *cli) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Fprintf(c.streams.Out, "evalvet %s (commit %s, built %s, %s %s/%s)\n",
				c.build.Version, c.build.Commit, c.build.Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}

func newPricingCmd(c *cli) *cobra.Command {
	cmd := &cobra.Command{Use: "pricing", Short: "Inspect the pricing table"}
	var file string
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective pricing table",
		RunE: func(_ *cobra.Command, _ []string) error {
			a, err := app.New(app.Options{PricingFile: file})
			if err != nil {
				return err
			}
			return printPricing(c, a.Pricing())
		},
	}
	show.Flags().StringVar(&file, "pricing", "", "pricing YAML layered over the built-in table")
	cmd.AddCommand(show)
	return cmd
}

func optRate(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.3f", *p)
}

func printPricing(c *cli, t pricing.Table) error {
	fmt.Fprintf(c.streams.Out, "pricing %s (%s per million tokens)\n\n", t.Version, t.Currency)
	tw := tabwriter.NewWriter(c.streams.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tMODEL\tFROM\tINPUT\tCACHED\tOUTPUT\tTIERS")
	for _, e := range t.Models {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.3f\t%s\t%.3f\t%d\n", e.Provider, e.Model, e.EffectiveFrom, e.PerMTok.Input, optRate(e.PerMTok.CachedInput), e.PerMTok.Output, len(e.Tiers))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("print pricing: %w", err)
	}
	return nil
}
