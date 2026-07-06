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
// by fingerprint. Events are processed in append order so a later finding event
// reactivates a fingerprint that an earlier resolved event may have closed,
// and a later resolved event closes a fingerprint that an earlier finding
// opened. This keeps live outcome aggregates consistent with `sieve sync`.
func Aggregate(events []Event) []CategoryStat {
	type state struct {
		cat, tier string
		conf      float64
		posted    bool
		addressed bool
		dismissed bool
		plus      int
		minus     int
	}

	byFp := map[string]*state{}
	for _, e := range events {
		s := byFp[e.Fp]
		if s == nil {
			s = &state{}
			byFp[e.Fp] = s
		}

		switch e.Type {
		case TypeFinding:
			// Latest finding wins for category/confidence/tier.
			s.cat = e.Cat
			s.conf = e.Conf
			s.tier = e.Tier
			if e.Tier == "inline" {
				s.posted = true
			}
			// A re-posted finding cancels any earlier resolution/dismissal.
			s.addressed = false
			s.dismissed = false
		case TypeResolved:
			if e.How == ResolvedAnchorGone {
				s.addressed = true
				// Addressed (a fix landed) takes precedence over dismissed (thread
				// closed without a fix).
				s.dismissed = false
			}
		case TypeReaction:
			// Latest snapshot wins (events are append-ordered), so re-running a
			// review never double-counts reactions.
			s.plus = e.Plus
			s.minus = e.Minus
		case TypeDismissed:
			if !s.addressed {
				s.dismissed = true
			}
		}
	}

	agg := map[string]*CategoryStat{}
	confAddr := map[string][]float64{}
	confIgn := map[string][]float64{}
	for _, s := range byFp {
		if !s.posted {
			continue
		}
		c := agg[s.cat]
		if c == nil {
			c = &CategoryStat{Category: s.cat}
			agg[s.cat] = c
		}
		c.Posted++
		c.PlusOne += s.plus
		c.MinusOne += s.minus
		if s.addressed {
			c.AddressedByAnchor++
			confAddr[s.cat] = append(confAddr[s.cat], s.conf)
		} else {
			confIgn[s.cat] = append(confIgn[s.cat], s.conf)
			if s.dismissed {
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
