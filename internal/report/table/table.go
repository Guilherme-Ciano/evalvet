package table

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/format"
)

func Write(w io.Writer, r domain.RunResult) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "suite %s · %d trials · %d cached\n\n", r.Suite, r.TotalTrials, r.CacheHits)
	fmt.Fprintln(tw, "TARGET\tMODEL\tPASS RATE\tCASES\tTRIALS\tERRORS\tCOST\tP50\tP95\tTTFT P50")
	for _, t := range r.Targets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\n",
			t.Target, t.Model, format.Rate(t.PassRate), len(t.Cases), t.Trials, t.Errors,
			format.Cost(t.CostUSD), format.Dur(t.LatencyP50), format.Dur(t.LatencyP95), format.Dur(t.TTFTP50))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	for _, c := range r.Comparisons {
		first, second := format.CompareLines(c)
		fmt.Fprintf(w, "\n%s: %s → %s (%s)\n  %s\n  %s\n", c.Name, c.Base, c.Head, c.Mode, first, second)
		for _, reason := range c.Reasons {
			fmt.Fprintf(w, "  · %s\n", reason)
		}
		shown := 0
		for _, cd := range c.Cases {
			if shown == 5 || cd.HeadPasses*cd.BaseN >= cd.BasePasses*cd.HeadN {
				continue
			}
			fmt.Fprintf(w, "    %-28s %d/%d → %d/%d  p_adj=%s  %s\n", cd.CaseID, cd.BasePasses, cd.BaseN, cd.HeadPasses, cd.HeadN, format.P(cd.PAdjusted), cd.FirstFailure)
			shown++
		}
	}
	for _, g := range r.Gates {
		fmt.Fprintf(w, "\ngate %s failed for %s: %s\n", g.Gate, g.Target, g.Message)
	}
	for _, warn := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	verdict := "PASS"
	if r.Failing() {
		verdict = "FAIL"
	}
	_, err := fmt.Fprintf(w, "\nresult: %s\n", verdict)
	if err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}
