package markdown

import (
	"fmt"
	"io"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/report/format"
)

func Marker(suite string) string {
	return "<!-- evalvet:" + suite + " -->"
}

func overall(r domain.RunResult) (string, string) {
	worst := domain.VerdictPass
	for _, c := range r.Comparisons {
		switch {
		case c.Verdict.Failing():
			worst = c.Verdict
		case c.Verdict == domain.VerdictInconclusive && !worst.Failing():
			worst = c.Verdict
		}
	}
	if len(r.Gates) > 0 && !worst.Failing() {
		return "❌", "gate failed"
	}
	if len(r.Comparisons) == 0 {
		return "✅", "no baseline to compare"
	}
	return format.VerdictIcon(worst), strings.ReplaceAll(string(worst), "_", "-")
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

func Write(w io.Writer, r domain.RunResult) error {
	var b strings.Builder
	icon, label := overall(r)
	fmt.Fprintf(&b, "%s\n### evalvet · %s · %s %s\n\n", Marker(r.Suite), r.Suite, icon, label)
	if len(r.Comparisons) > 0 {
		b.WriteString("| comparison | pass rate (base → head) | Δ (95% CI) | p | cost | p95 latency | TTFT p50 | verdict |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.Comparisons {
			fmt.Fprintf(&b, "| %s → %s | %s → %s | %s [%s, %s] | %s | %s → %s | %s → %s | %s | %s %s |\n",
				c.Base, c.Head, format.Rate(c.BasePassRate), format.Rate(c.HeadPassRate),
				format.Signed(c.Delta), format.Signed(c.CILow), format.Signed(c.CIHigh), format.P(c.P),
				format.Cost(c.BaseCost), format.Cost(c.HeadCost), format.Dur(c.BaseP95), format.Dur(c.HeadP95),
				format.Dur(c.HeadTTFTP50), format.VerdictIcon(c.Verdict), c.Verdict)
		}
		b.WriteString("\n")
	}
	b.WriteString("| target | model | pass rate | cases | trials | errors | cost |\n|---|---|---|---|---|---|---|\n")
	for _, t := range r.Targets {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %s |\n", t.Target, t.Model, format.Rate(t.PassRate), len(t.Cases), t.Trials, t.Errors, format.Cost(t.CostUSD))
	}
	cost := "n/a"
	if r.TotalCost != nil {
		cost = format.Cost(r.TotalCost)
	}
	hitPct := 0
	if r.TotalTrials > 0 {
		hitPct = r.CacheHits * 100 / r.TotalTrials
	}
	fmt.Fprintf(&b, "\n%d trials, %d%% cache hits, live spend %s.\n", r.TotalTrials, hitPct, cost)
	for _, c := range r.Comparisons {
		if len(c.Reasons) > 0 {
			fmt.Fprintf(&b, "\n**%s → %s:** %s\n", c.Base, c.Head, escape(strings.Join(c.Reasons, "; ")))
		}
	}
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "\n**Gate `%s` failed** for `%s`: %s\n", g.Gate, g.Target, escape(g.Message))
	}
	for _, c := range r.Comparisons {
		var rows []domain.CaseDiff
		for _, cd := range c.Cases {
			if cd.HeadPasses*cd.BaseN < cd.BasePasses*cd.HeadN && len(rows) < 15 {
				rows = append(rows, cd)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n<details><summary>%s → %s: cases driving the change (Holm-adjusted)</summary>\n\n", c.Base, c.Head)
		b.WriteString("| case | base | head | p_adj | first failing assertion |\n|---|---|---|---|---|\n")
		for _, cd := range rows {
			fmt.Fprintf(&b, "| %s | %d/%d | %d/%d | %s | %s |\n", escape(cd.CaseID), cd.BasePasses, cd.BaseN, cd.HeadPasses, cd.HeadN, format.P(cd.PAdjusted), escape(cd.FirstFailure))
		}
		b.WriteString("</details>\n")
	}
	m := r.Method
	fmt.Fprintf(&b, "\n<details><summary>Method</summary>Paired sign-flip permutation test on per-case pass rates (%d rounds, exact when N ≤ 20, seed %d), case-clustered bootstrap 95%% CI (%d resamples), α=%.2f, δ=%.2f, per-case one-sided Fisher with Holm correction, pricing %s.</details>\n",
		m.Permutations, m.Seed, m.Bootstrap, m.Alpha, m.MinEffect, m.PricingVersion)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write markdown: %w", err)
	}
	return nil
}
