package stats

import (
	"math"
	"math/rand/v2"
	"slices"
	"sort"
)

const (
	DefaultPermutations = 10000
	DefaultBootstrap    = 10000
	exactLimit          = 20
)

func NormalQuantile(p float64) float64 {
	return math.Sqrt2 * math.Erfinv(2*p-1)
}

func Wilson(passes, n int, confidence float64) (low, high float64) {
	if n <= 0 {
		return 0, 1
	}
	z := NormalQuantile(1 - (1-confidence)/2)
	nf := float64(n)
	p := float64(passes) / nf
	z2 := z * z
	den := 1 + z2/nf
	center := (p + z2/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / den
	return math.Max(0, center-half), math.Min(1, center+half)
}

func logChoose(n, k int) float64 {
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

func hypergeom(k, rowA, rowB, colPass int) float64 {
	return math.Exp(logChoose(rowA, k) + logChoose(rowB, colPass-k) - logChoose(rowA+rowB, colPass))
}

func FisherLess(basePasses, baseN, headPasses, headN int) float64 {
	if baseN <= 0 || headN <= 0 {
		return 1
	}
	colPass := basePasses + headPasses
	lo := max(0, colPass-baseN)
	p := 0.0
	for k := lo; k <= headPasses; k++ {
		p += hypergeom(k, headN, baseN, colPass)
	}
	return math.Min(1, p)
}

func Holm(p []float64) []float64 {
	m := len(p)
	idx := make([]int, m)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })
	out := make([]float64, m)
	running := 0.0
	for rank, i := range idx {
		adj := math.Min(1, float64(m-rank)*p[i])
		running = math.Max(running, adj)
		out[i] = running
	}
	return out
}

func BenjaminiHochberg(p []float64) []float64 {
	m := len(p)
	idx := make([]int, m)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]] > p[idx[b]] })
	out := make([]float64, m)
	running := 1.0
	for pos, i := range idx {
		rank := m - pos
		adj := math.Min(1, p[i]*float64(m)/float64(rank))
		running = math.Min(running, adj)
		out[i] = running
	}
	return out
}

func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

type PermutationResult struct {
	PLess    float64
	PGreater float64
	Exact    bool
}

func PairedPermutation(d []float64, rounds int, seed uint64) PermutationResult {
	n := len(d)
	if n == 0 {
		return PermutationResult{PLess: 1, PGreater: 1, Exact: true}
	}
	observed := sum(d)
	if n <= exactLimit {
		return exactPermutation(d, observed)
	}
	if rounds <= 0 {
		rounds = DefaultPermutations
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	less, greater := 0, 0
	const eps = 1e-12
	for range rounds {
		s := 0.0
		for _, x := range d {
			if rng.Uint64()&1 == 1 {
				s -= x
			} else {
				s += x
			}
		}
		if s <= observed+eps {
			less++
		}
		if s >= observed-eps {
			greater++
		}
	}
	return PermutationResult{
		PLess:    float64(less+1) / float64(rounds+1),
		PGreater: float64(greater+1) / float64(rounds+1),
	}
}

func sum(d []float64) float64 {
	s := 0.0
	for _, x := range d {
		s += x
	}
	return s
}

func exactPermutation(d []float64, observed float64) PermutationResult {
	n := len(d)
	total := 1 << n
	less, greater := 0, 0
	const eps = 1e-12
	for mask := range total {
		s := 0.0
		for i, x := range d {
			if mask&(1<<i) != 0 {
				s -= x
			} else {
				s += x
			}
		}
		if s <= observed+eps {
			less++
		}
		if s >= observed-eps {
			greater++
		}
	}
	return PermutationResult{
		PLess:    float64(less) / float64(total),
		PGreater: float64(greater) / float64(total),
		Exact:    true,
	}
}

func BootstrapMeanCI(d []float64, rounds int, seed uint64, confidence float64) (low, high float64) {
	n := len(d)
	if n == 0 {
		return 0, 0
	}
	if rounds <= 0 {
		rounds = DefaultBootstrap
	}
	rng := rand.New(rand.NewPCG(seed, seed^0xda942042e4dd58b5))
	means := make([]float64, rounds)
	for r := range rounds {
		s := 0.0
		for range n {
			s += d[rng.IntN(n)]
		}
		means[r] = s / float64(n)
	}
	slices.Sort(means)
	alpha := (1 - confidence) / 2
	return quantileSorted(means, alpha), quantileSorted(means, 1-alpha)
}

func quantileSorted(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	pos := q * float64(len(xs)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return xs[lo]
	}
	frac := pos - float64(lo)
	return xs[lo]*(1-frac) + xs[hi]*frac
}

func Quantile(xs []float64, q float64) float64 {
	c := slices.Clone(xs)
	slices.Sort(c)
	return quantileSorted(c, q)
}

func SampleSizePerArm(p1, p2, alpha, power float64) int {
	if p1 == p2 {
		return math.MaxInt32
	}
	za := NormalQuantile(1 - alpha)
	zb := NormalQuantile(power)
	pbar := (p1 + p2) / 2
	num := za*math.Sqrt(2*pbar*(1-pbar)) + zb*math.Sqrt(p1*(1-p1)+p2*(1-p2))
	diff := p1 - p2
	return int(math.Ceil(num * num / (diff * diff)))
}

func MinimumDetectableEffect(cases int, k, baseRate, alpha, power float64) float64 {
	if cases <= 0 || k <= 0 {
		return 1
	}
	p := math.Min(math.Max(baseRate, 0.05), 0.95)
	se := math.Sqrt(2 * p * (1 - p) / (float64(cases) * k))
	return (NormalQuantile(1-alpha) + NormalQuantile(power)) * se
}
