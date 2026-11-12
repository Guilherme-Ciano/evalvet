package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Guilherme-Ciano/evalvet/internal/app"
)

func newValidateCmd(c *cli) *cobra.Command {
	var pricingFile string
	cmd := &cobra.Command{
		Use:   "validate [suite.eval.yaml...]",
		Short: "Check suites without calling any model",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			a, err := app.New(app.Options{PricingFile: pricingFile, Env: c.env})
			if err != nil {
				return err
			}
			var errs []error
			for _, p := range args {
				s, err := a.Load(p)
				if err != nil {
					fmt.Fprintln(c.streams.Err, err)
					errs = append(errs, err)
					continue
				}
				fmt.Fprintf(c.streams.Out, "%s: ok (%d cases, %d targets)\n", p, len(s.Cases), len(s.Matrix.Targets))
				for _, w := range a.Warnings(s) {
					fmt.Fprintf(c.streams.Out, "  warning: %s\n", w)
				}
			}
			if len(errs) > 0 {
				return &ExitError{Code: ExitUsage, Err: fmt.Errorf("%d of %d suite(s) invalid", len(errs), len(args))}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&pricingFile, "pricing", "", "pricing YAML layered over the built-in table")
	return cmd
}
