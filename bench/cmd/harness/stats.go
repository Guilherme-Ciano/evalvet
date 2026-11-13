package main

import (
	"math"
	"slices"
)

type Dist struct {
	N      int     `json:"n"`
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Mean   float64 `json:"mean"`
	P90    float64 `json:"p90"`
	P99    float64 `json:"p99"`
	Max    float64 `json:"max"`
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	frac := pos - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac
}

func dist(xs []float64) Dist {
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 0 {
		return Dist{}
	}
	var sum float64
	for _, x := range s {
		sum += x
	}
	return Dist{
		N: len(s), Min: s[0], Median: quantile(s, 0.5), Mean: sum / float64(len(s)),
		P90: quantile(s, 0.9), P99: quantile(s, 0.99), Max: s[len(s)-1],
	}
}
