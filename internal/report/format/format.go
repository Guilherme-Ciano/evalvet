package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func clean(v float64) float64 {
	r := math.Round(v*100) / 100
	if r == 0 {
		return 0
	}
	return r
}

func Rate(v float64) string {
	return fmt.Sprintf("%.2f", clean(v))
}

func Signed(v float64) string {
	v = clean(v)
	switch {
	case v > 0:
		return fmt.Sprintf("+%.2f", v)
	case v < 0:
		return fmt.Sprintf("−%.2f", -v)
	default:
		return "0.00"
	}
}

func P(p float64) string {
	switch {
	case p < 0.001:
		return "<0.001"
	case p < 0.01:
		return fmt.Sprintf("%.3f", p)
	default:
		return fmt.Sprintf("%.2f", clean(p))
	}
}

func Cost(c *float64) string {
	if c == nil {
		return "n/a"
	}
	return fmt.Sprintf("$%.2f", clean(*c))
}

func Pct(base, head *float64) string {
	if base == nil || head == nil || *base == 0 {
		return ""
	}
	pct := (*head - *base) / *base * 100
	r := math.Round(pct)
	switch {
	case r > 0:
		return fmt.Sprintf(" (+%.0f%%)", r)
	case r < 0:
		return fmt.Sprintf(" (−%.0f%%)", -r)
	default:
		return " (0%)"
	}
}

func Dur(d time.Duration) string {
	switch {
	case d <= 0:
		return "n/a"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Round(time.Millisecond).Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

func VerdictLabel(v domain.Verdict) string {
	return strings.ToUpper(strings.ReplaceAll(string(v), "_", "-"))
}

func VerdictIcon(v domain.Verdict) string {
	switch v {
	case domain.VerdictRegression, domain.VerdictNotNonInferior:
		return "❌"
	case domain.VerdictInconclusive:
		return "⚠️"
	case domain.VerdictImprovement:
		return "🚀"
	default:
		return "✅"
	}
}

func CompareLines(c domain.Comparison) (string, string) {
	k := fmt.Sprintf("%.1f", c.MeanK)
	if c.MeanK == math.Trunc(c.MeanK) {
		k = strconv.Itoa(int(c.MeanK))
	}
	first := fmt.Sprintf("pass rate %s → %s (Δ %s, 95%% CI [%s, %s]), p=%s, N=%d cases × k=%s · %s",
		Rate(c.BasePassRate), Rate(c.HeadPassRate), Signed(c.Delta), Signed(c.CILow), Signed(c.CIHigh),
		P(c.P), c.N, k, VerdictLabel(c.Verdict))
	second := fmt.Sprintf("cost %s → %s%s · p50 latency %s → %s · TTFT p50 %s → %s",
		Cost(c.BaseCost), Cost(c.HeadCost), Pct(c.BaseCost, c.HeadCost),
		Dur(c.BaseP50), Dur(c.HeadP50), Dur(c.BaseTTFTP50), Dur(c.HeadTTFTP50))
	return first, second
}
