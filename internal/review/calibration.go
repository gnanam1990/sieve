package review

import (
	"strings"

	"github.com/gnanam1990/sieve/internal/memory"
)

// applyCalibration (opt-in, review.calibration) scales each finding's
// confidence by its category's addressed-rate factor from the local outcome
// store, before the gate sees it. It is transparent: every adjusted finding is
// recorded raw-vs-calibrated in rc.Calibration. A category below the sample
// floor (or an unreadable store) is a no-op (factor 1.0).
func applyCalibration(rc *ReviewContext, opts Options) {
	owner, name, _ := strings.Cut(rc.Repo, "/")
	store := memory.Open(memoryHost, owner, name, opts.Log)
	events, _, err := store.Read()
	if err != nil {
		opts.Log.Warn("calibration: could not read outcome store; skipping", "err", err)
		return
	}
	stats := memory.Aggregate(events)
	for i := range rc.Findings {
		f := &rc.Findings[i]
		factor := memory.CalibrationFactor(stats, f.Category)
		if factor == 1.0 {
			continue
		}
		raw := f.Confidence
		f.Confidence = raw * factor
		rc.Calibration = append(rc.Calibration, CalibrationRecord{
			Path: f.Path, Line: f.Line, Category: f.Category,
			Raw: raw, Calibrated: f.Confidence, Factor: factor,
		})
	}
}
