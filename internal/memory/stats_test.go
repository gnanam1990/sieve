package memory

import "testing"

// buildEvents makes n inline findings in a category, marking `addressed` of
// them resolved via anchor-gone, with the given per-finding confidences.
func inlineFinding(fp, cat string, conf float64) Event {
	return Event{Type: TypeFinding, Fp: fp, Cat: cat, Tier: "inline", Conf: conf}
}

func TestAggregateAddressedRate(t *testing.T) {
	events := []Event{
		inlineFinding("a", "bug", 0.9),
		inlineFinding("b", "bug", 0.8),
		inlineFinding("c", "bug", 0.7),
		inlineFinding("d", "style", 0.6), // notes-only style would not count; this is inline
		{Type: TypeResolved, Fp: "a", How: ResolvedAnchorGone},
		{Type: TypeResolved, Fp: "b", How: ResolvedReReviewAbsent}, // not an "addressed" fix
		{Type: TypeReaction, Fp: "a", Plus: 1},
		{Type: TypeReaction, Fp: "c", Minus: 1},
		{Type: TypeReaction, Fp: "c", Minus: 1}, // re-run snapshot: latest wins, still 1
		{Type: TypeDismissed, Fp: "c"},
	}
	stats := Aggregate(events)
	var bug CategoryStat
	for _, s := range stats {
		if s.Category == "bug" {
			bug = s
		}
	}
	if bug.Posted != 3 {
		t.Fatalf("bug posted = %d, want 3", bug.Posted)
	}
	if bug.AddressedByAnchor != 1 { // only "a" resolved via anchor-gone
		t.Fatalf("addressed = %d, want 1", bug.AddressedByAnchor)
	}
	if bug.PlusOne != 1 || bug.MinusOne != 1 || bug.Dismissed != 1 {
		t.Fatalf("reactions/dismissed wrong: %+v", bug)
	}
	if r := bug.AddressedRate(); r < 0.33 || r > 0.34 {
		t.Fatalf("addressed rate = %v, want ~0.333", r)
	}
	if bug.ReactionScore() != 0 {
		t.Fatalf("reaction score = %d, want 0", bug.ReactionScore())
	}
	// mean conf: addressed = {0.9}; ignored = {0.8, 0.7}
	if bug.MeanConfAddressed != 0.9 {
		t.Fatalf("mean conf addressed = %v, want 0.9", bug.MeanConfAddressed)
	}
	if bug.MeanConfIgnored < 0.74 || bug.MeanConfIgnored > 0.76 {
		t.Fatalf("mean conf ignored = %v, want 0.75", bug.MeanConfIgnored)
	}
}

// TestAddressedExcludesDismissed: a finding dismissed then fixed counts as
// addressed only (not double-counted), matching what sync can reconstruct.
func TestAddressedExcludesDismissed(t *testing.T) {
	stats := Aggregate([]Event{
		inlineFinding("d", "bug", 0.9),
		{Type: TypeDismissed, Fp: "d"},                              // dismissed while active...
		{Type: TypeResolved, Fp: "d", How: ResolvedAnchorGone},     // ...then fixed
	})
	if len(stats) != 1 {
		t.Fatalf("want 1 category, got %d", len(stats))
	}
	if stats[0].AddressedByAnchor != 1 || stats[0].Dismissed != 0 {
		t.Fatalf("fixed finding must be addressed not dismissed: %+v", stats[0])
	}
}

// TestReactionRemovalLatestWins: a later 0/0 snapshot supersedes an earlier
// non-zero one, so a removed reaction is not left stale.
func TestReactionRemovalLatestWins(t *testing.T) {
	stats := Aggregate([]Event{
		inlineFinding("r", "bug", 0.9),
		{Type: TypeReaction, Fp: "r", Plus: 5}, // earlier snapshot
		{Type: TypeReaction, Fp: "r", Plus: 0}, // reaction removed
	})
	if stats[0].PlusOne != 0 {
		t.Fatalf("removed reaction must not stay counted, got %d", stats[0].PlusOne)
	}
}

func TestAggregateSkipsNotesOnly(t *testing.T) {
	events := []Event{{Type: TypeFinding, Fp: "n", Cat: "style", Tier: "notes", Conf: 0.5}}
	stats := Aggregate(events)
	if len(stats) != 0 {
		t.Fatalf("notes-only findings must not count as posted: %+v", stats)
	}
}

func TestAggregateDedupesByFp(t *testing.T) {
	// The same finding recorded across two runs counts once.
	events := []Event{
		inlineFinding("x", "bug", 0.9),
		inlineFinding("x", "bug", 0.9),
	}
	stats := Aggregate(events)
	if len(stats) != 1 || stats[0].Posted != 1 {
		t.Fatalf("dedupe by fp failed: %+v", stats)
	}
}

func TestCalibrationFactor(t *testing.T) {
	// Below MinSample: no-op 1.0.
	small := []CategoryStat{{Category: "bug", Posted: 5, AddressedByAnchor: 1}}
	if f := CalibrationFactor(small, "bug"); f != 1.0 {
		t.Fatalf("n<10 must be a no-op, got %v", f)
	}
	// addressed_rate 0.5 -> factor 1.0.
	half := []CategoryStat{{Category: "bug", Posted: 10, AddressedByAnchor: 5}}
	if f := CalibrationFactor(half, "bug"); f != 1.0 {
		t.Fatalf("rate 0.5 -> 1.0, got %v", f)
	}
	// addressed_rate 0.1 -> factor clamp(0.2, .5, 1) = 0.5.
	low := []CategoryStat{{Category: "bug", Posted: 10, AddressedByAnchor: 1}}
	if f := CalibrationFactor(low, "bug"); f != 0.5 {
		t.Fatalf("low rate must clamp to 0.5, got %v", f)
	}
	// addressed_rate 0.9 -> clamp(1.8,.5,1) = 1.0.
	high := []CategoryStat{{Category: "bug", Posted: 10, AddressedByAnchor: 9}}
	if f := CalibrationFactor(high, "bug"); f != 1.0 {
		t.Fatalf("high rate stays 1.0, got %v", f)
	}
	// Unknown category -> 1.0.
	if f := CalibrationFactor(half, "unknown"); f != 1.0 {
		t.Fatalf("unknown category -> 1.0, got %v", f)
	}
}
