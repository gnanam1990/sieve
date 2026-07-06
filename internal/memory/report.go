package memory

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Totals summarizes run-level telemetry across the log.
type Totals struct {
	Runs   int
	InTok  int
	OutTok int
}

// Sum computes run totals from the event log.
func Sum(events []Event) Totals {
	var t Totals
	for _, e := range events {
		if e.Type == TypeRun {
			t.Runs++
			t.InTok += e.InTok
			t.OutTok += e.OutTok
		}
	}
	return t
}

// WriteStats renders the per-category table + totals as a tabwriter table
// matching sieve's summary style (no color).
func WriteStats(w io.Writer, stats []CategoryStat, totals Totals) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "CATEGORY\tPOSTED\tADDRESSED\t👍\t👎\tDISMISSED\tCONF(addr/ign)")
	var posted, addressed, plus, minus, dismissed int
	for _, c := range stats {
		rate := "-"
		if c.Posted >= MinSample {
			rate = fmt.Sprintf("%.0f%%", c.AddressedRate()*100)
		}
		fmt.Fprintf(tw, "%s\t%d\t%d (%s)\t%d\t%d\t%d\t%.2f/%.2f\n",
			c.Category, c.Posted, c.AddressedByAnchor, rate, c.PlusOne, c.MinusOne, c.Dismissed,
			c.MeanConfAddressed, c.MeanConfIgnored)
		posted += c.Posted
		addressed += c.AddressedByAnchor
		plus += c.PlusOne
		minus += c.MinusOne
		dismissed += c.Dismissed
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%d\t%d\t%d\t\n", posted, addressed, plus, minus, dismissed)
	tw.Flush() //nolint:errcheck // best-effort human output
	fmt.Fprintf(w, "\n%d runs · tokens in %d / out %d\n", totals.Runs, totals.InTok, totals.OutTok)
}

// WriteStatsJSON emits the stats as JSON (--json).
func WriteStatsJSON(w io.Writer, stats []CategoryStat, totals Totals) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Categories []CategoryStat
		Totals     Totals
	}{stats, totals})
}
