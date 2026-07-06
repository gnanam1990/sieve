package findings

import (
	"fmt"

	"github.com/gnanam1990/sieve/internal/diff"
)

// Anchors indexes every commentable (path, side, line) of a review's kept
// files, per hunk. GitHub multi-line comments cannot span hunks, so range
// validation requires a single hunk to cover [Line, EndLine].
type Anchors struct {
	files map[string][]hunkAnchors
}

type hunkAnchors struct {
	right map[int]bool // NewNum of Added + Context lines
	left  map[int]bool // OldNum of Removed + Context lines
}

// NewAnchors builds the index from kept (non-skipped) file diffs. The path
// key is NewPath, falling back to OldPath for deletes.
func NewAnchors(kept []diff.FileDiff) *Anchors {
	a := &Anchors{files: make(map[string][]hunkAnchors, len(kept))}
	for _, fd := range kept {
		path := fd.NewPath
		if path == "" {
			path = fd.OldPath
		}
		hunks := make([]hunkAnchors, 0, len(fd.Hunks))
		for _, h := range fd.Hunks {
			ha := hunkAnchors{right: map[int]bool{}, left: map[int]bool{}}
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.AddedLine:
					ha.right[l.NewNum] = true
				case diff.RemovedLine:
					ha.left[l.OldNum] = true
				case diff.Context:
					ha.right[l.NewNum] = true
					ha.left[l.OldNum] = true
				}
			}
			hunks = append(hunks, ha)
		}
		a.files[path] = hunks
	}
	return a
}

// Validate rejects any finding whose shape is invalid or whose anchor does
// not land on commentable diff lines. Invalid findings must be dropped by
// the caller — never repaired or re-anchored.
func (a *Anchors) Validate(f Finding) error {
	if err := validateShape(f); err != nil {
		return err
	}
	hunks, ok := a.files[f.Path]
	if !ok {
		return fmt.Errorf("path %q is not a reviewed file", f.Path)
	}
	end := f.EndLine
	if end == 0 {
		end = f.Line
	}
	for _, h := range hunks {
		lines := h.right
		if f.Side == SideLeft {
			lines = h.left
		}
		covered := true
		for n := f.Line; n <= end; n++ {
			if !lines[n] {
				covered = false
				break
			}
		}
		if covered {
			return nil
		}
	}
	if f.EndLine != 0 {
		return fmt.Errorf("range %s:%d-%d (%s) is not commentable within a single hunk", f.Path, f.Line, f.EndLine, f.Side)
	}
	return fmt.Errorf("line %s:%d (%s) is not a commentable diff line", f.Path, f.Line, f.Side)
}
