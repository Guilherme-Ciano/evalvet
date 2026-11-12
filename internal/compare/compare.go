package compare

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/stats"
)

const (
	ModeRegression     = "regression"
	ModeNonInferiority = "noninferiority"
)

type Options struct {
	Alpha        float64
	MinEffect    float64
	Mode         string
	Margin       float64
	Seed         uint64
	Permutations int
	Bootstrap    int
	Power        float64
}

func streamSeed(seed uint64, name string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	return seed ^ h.Sum64()
}

func Compare(name string, base, head domain.TargetStats, o Options) domain.Comparison {
	if o.Mode == "" {
		o.Mode = ModeRegression
	}
	if o.Margin <= 0 {
		o.Margin = o.MinEffect
	}
	if o.Power <= 0 {
		o.Power = 0.8
	}
	c := domain.Comparison{
		Name: name, Base: base.Target, Head: head.Target, Mode: o.Mode,
		BaseCost: base.CostUSD, HeadCost: head.CostUSD,
		BaseP50: base.LatencyP50, HeadP50: head.LatencyP50,
		BaseP95: base.LatencyP95, HeadP95: head.LatencyP95,
		BaseTTFTP50: base.TTFTP50, HeadTTFTP50: head.TTFTP50,
	}
	baseByID := map[string]domain.CaseStats{}
	for _, cs := range base.Cases {
		baseByID[cs.CaseID] = cs
	}
	var (
		d         []float64
		bRates    []float64
		hRates    []float64
		kSum      float64
		allKOne   = true
		diffs     []domain.CaseDiff
		dropped   int
		pvals     []float64
	)
	for _, h := range head.Cases {
		b, ok := baseByID[h.CaseID]
		if !ok || b.N == 0 || h.N == 0 {
			dropped++
			continue
		}
		d = append(d, h.PassRate()-b.PassRate())
		bRates = append(bRates, b.PassRate())
		hRates = append(hRates, h.PassRate())
		kSum += float64(h.N+b.N) / 2
		if h.N != 1 || b.N != 1 {
			allKOne = false
		}
		p := stats.FisherLess(b.Passes, b.N, h.Passes, h.N)
		pvals = append(pvals, p)
		diffs = append(diffs, domain.CaseDiff{
			CaseID: h.CaseID, Line: h.Line,
			BasePasses: b.Passes, BaseN: b.N, HeadPasses: h.Passes, HeadN: h.N,
			P: p, FirstFailure: h.FirstFailure,
		})
	}
	c.N = len(d)
	if dropped > 0 {
		c.Reasons = append(c.Reasons, fmt.Sprintf("%d case(s) without valid trials in both arms were excluded", dropped))
	}
	if c.N == 0 {
		c.Verdict = domain.VerdictInconclusive
		c.Reasons = append(c.Reasons, "no paired cases")
		c.P, c.PImprove = 1, 1
		return c
	}
	c.MeanK = kSum / float64(c.N)
	c.BasePassRate = stats.Mean(bRates)
	c.HeadPassRate = stats.Mean(hRates)
	c.Delta = stats.Mean(d)
	seed := streamSeed(o.Seed, name)
	perm := stats.PairedPermutation(d, o.Permutations, seed)
	c.P, c.PImprove, c.Exact = perm.PLess, perm.PGreater, perm.Exact
	c.Test = "paired sign-flip permutation"
	if allKOne {
		c.Test = "McNemar (exact, via sign-flip)"
	}
	if perm.Exact {
		c.Test += ", exact"
	}
	c.CILow, c.CIHigh = stats.BootstrapMeanCI(d, o.Bootstrap, seed+1, 0.95)
	c.MDE = stats.MinimumDetectableEffect(c.N, c.MeanK, c.BasePassRate, o.Alpha, o.Power)
	adj := stats.Holm(pvals)
	for i := range diffs {
		diffs[i].PAdjusted = adj[i]
	}
	sort.SliceStable(diffs, func(a, b int) bool { return diffs[a].PAdjusted < diffs[b].PAdjusted })
	c.Cases = diffs
	c.Verdict, c.Reasons = decide(c, o, c.Reasons)
	return c
}

func decide(c domain.Comparison, o Options, reasons []string) (domain.Verdict, []string) {
	if o.Mode == ModeNonInferiority {
		if c.CILow > -o.Margin {
			return domain.VerdictNonInferior, append(reasons, fmt.Sprintf("CI lower bound %+.3f > −%.2f margin", c.CILow, o.Margin))
		}
		return domain.VerdictNotNonInferior, append(reasons, fmt.Sprintf("CI lower bound %+.3f ≤ −%.2f margin", c.CILow, o.Margin))
	}
	switch {
	case c.P < o.Alpha && c.Delta < -o.MinEffect:
		return domain.VerdictRegression, append(reasons, fmt.Sprintf("p=%.4f < α=%.2f and Δ %+.3f beyond −%.2f", c.P, o.Alpha, c.Delta, o.MinEffect))
	case c.PImprove < o.Alpha && c.Delta > o.MinEffect:
		return domain.VerdictImprovement, append(reasons, fmt.Sprintf("p=%.4f < α=%.2f and Δ %+.3f beyond +%.2f", c.PImprove, o.Alpha, c.Delta, o.MinEffect))
	case c.CILow < -o.MinEffect:
		r := fmt.Sprintf("CI [%+.3f, %+.3f] straddles −%.2f; this suite can only detect drops ≥ %.0f pts", c.CILow, c.CIHigh, o.MinEffect, math.Ceil(c.MDE*100))
		return domain.VerdictInconclusive, append(reasons, r)
	default:
		return domain.VerdictPass, reasons
	}
}
