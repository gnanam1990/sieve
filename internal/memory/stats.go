package memory

import "sort"

// MinSample is the minimum number of posted inline findings in a category
// before its addressed-rate is considered meaningful (calibration + reporting).
const MinSample = 10

// CategoryStat aggregates outcomes for one finding category.
type CategoryStat struct {
	Category          string
	Posted            int     // distinct findings posted inline
	AddressedByAnchor int     // of Posted, resolved via anchor-gone (a fix)
	PlusOne           int     // 👍 reactions
	MinusOne          int     // 👎 reactions
	Dismissed         int     // inline threads a human resolved without a fix
	MeanConfAddressed float64 // mean confidence of addressed findings
	MeanConfIgnored   float64 // mean confidence of posted-but-not-addressed findings
}

// AddressedRate is AddressedByAnchor / Posted (0 when nothing posted).
func (c CategoryStat) AddressedRate() float64 {
	if c.Posted == 0 {
		return 0
	}
	return float64(c.AddressedByAnchor) / float64(c.Posted)
}

// ReactionScore is 👍 minus 👎.
func (c CategoryStat) ReactionScore() int { return c.PlusOne - c.MinusOne }

type findingAgg struct {
	cat    string
	conf   float64
	posted bool
}

// Aggregate collapses the event log into per-category stats, deduping findings
// by fingerprint (the latest finding event wins for category/confidence).
func Aggregate(events []Event) []CategoryStat {
	byFp := map[string]*findingAgg{}
	for _, e := range events {
		if e.Type != TypeFinding {
			continue
		}
		f := byFp[e.Fp]
		if f == nil {
			f = &findingAgg{}
			byFp[e.Fp] = f
		}
		f.cat = e.Cat
		f.conf = e.Conf
		if e.Tier == "inline" {
			f.posted = true
		}
	}
	addressed := map[string]bool{}
	plus := map[string]int{}
	minus := map[string]int{}
	dismissed := map[string]bool{}
	for _, e := range events {
		switch e.Type {
		case TypeResolved:
			if e.How == ResolvedAnchorGone {
				addressed[e.Fp] = true
			}
		case TypeReaction:
			// Latest snapshot wins (events are append-ordered), so re-running a
			// review never double-counts reactions.
			plus[e.Fp] = e.Plus
			minus[e.Fp] = e.Minus
		case TypeDismissed:
			dismissed[e.Fp] = true
		}
	}

	agg := map[string]*CategoryStat{}
	confAddr := map[string][]float64{}
	confIgn := map[string][]float64{}
	for fp, f := range byFp {
		if !f.posted {
			continue
		}
		c := agg[f.cat]
		if c == nil {
			c = &CategoryStat{Category: f.cat}
			agg[f.cat] = c
		}
		c.Posted++
		c.PlusOne += plus[fp]
		c.MinusOne += minus[fp]
		// Addressed (a fix landed) takes precedence over dismissed (thread
		// closed without a fix): a finding later fixed was addressed, not
		// dismissed. Keeping them mutually exclusive both avoids double-counting
		// and lets `sieve sync` — which sees only the current fixed state —
		// reconstruct the same numbers.
		if addressed[fp] {
			c.AddressedByAnchor++
			confAddr[f.cat] = append(confAddr[f.cat], f.conf)
		} else {
			confIgn[f.cat] = append(confIgn[f.cat], f.conf)
			if dismissed[fp] {
				c.Dismissed++
			}
		}
	}

	out := make([]CategoryStat, 0, len(agg))
	for cat, c := range agg {
		c.MeanConfAddressed = mean(confAddr[cat])
		c.MeanConfIgnored = mean(confIgn[cat])
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// CalibrationFactor returns the confidence multiplier for a category:
// clamp(addressed_rate / 0.5, 0.5, 1.0), or 1.0 (no-op) below MinSample. A
// category addressing exactly half its findings keeps full confidence; a lower
// rate scales confidence down (never below 0.5), a higher rate stays at 1.0.
func CalibrationFactor(stats []CategoryStat, category string) float64 {
	for _, c := range stats {
		if c.Category != category {
			continue
		}
		if c.Posted < MinSample {
			return 1.0
		}
		return clamp(c.AddressedRate()/0.5, 0.5, 1.0)
	}
	return 1.0
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
