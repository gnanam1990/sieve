// Package fingerprint computes content-anchored identifiers for findings so
// posting can be deduplicated across runs.
//
// A fingerprint deliberately excludes line numbers: it hashes the anchor
// line's *content*, so a finding that merely drifts to a new position (an
// edit above it shifted the line) keeps the same fingerprint and is not
// re-posted. Editing the anchored line, rewriting the title, or renaming the
// file all change the fingerprint — a rename yields a new fingerprint, which
// is accepted (documented) rather than chased with rename detection.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/gnanam1990/sieve/internal/diff"
)

// Len is the number of leading hex characters kept from the sha256 digest.
// 16 hex chars = 64 bits; collision odds across a single PR's findings are
// negligible.
const Len = 16

// For computes the fingerprint:
//
//	hex(sha256(path | side | category | norm(title) | trim(anchor)))[:16]
//
// where norm lowercases the title and collapses every non-alphanumeric run to
// a single space, and anchor is the diff content of the anchored line.
func For(path, side, category, title, anchor string) string {
	joined := strings.Join([]string{
		path,
		side,
		category,
		normTitle(title),
		strings.TrimSpace(anchor),
	}, "|")
	sum := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(sum[:])[:Len]
}

// normTitle lowercases s and reduces it to alphanumerics separated by single
// spaces (leading/trailing space trimmed). "Unchecked error from Close()" and
// "unchecked  error from  close" both normalize to "unchecked error from close".
func normTitle(s string) string {
	var b strings.Builder
	pendingSpace := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		default:
			pendingSpace = true
		}
	}
	return b.String()
}

// ContentIndex recovers the anchor-line content for a (path, side, line)
// without an extra fetch — the content is already present in the stage-1
// parsed diff.
type ContentIndex struct {
	right map[key]string // path+NewNum -> content (Added, Context)
	left  map[key]string // path+OldNum -> content (Removed, Context)
}

type key struct {
	path string
	line int
}

// NewContentIndex builds the index from the kept (non-skipped) file diffs.
// The path key is NewPath, falling back to OldPath for deletes — matching the
// finding anchor model.
func NewContentIndex(kept []diff.FileDiff) *ContentIndex {
	ci := &ContentIndex{right: map[key]string{}, left: map[key]string{}}
	for _, fd := range kept {
		path := fd.NewPath
		if path == "" {
			path = fd.OldPath
		}
		for _, h := range fd.Hunks {
			for _, l := range h.Lines {
				switch l.Kind {
				case diff.AddedLine:
					ci.right[key{path, l.NewNum}] = l.Content
				case diff.RemovedLine:
					ci.left[key{path, l.OldNum}] = l.Content
				case diff.Context:
					ci.right[key{path, l.NewNum}] = l.Content
					ci.left[key{path, l.OldNum}] = l.Content
				}
			}
		}
	}
	return ci
}

// Anchor returns the anchor line's content for (path, side, line). For a
// multi-line finding the caller passes the range start (Line), matching R4's
// "first line of the range". A miss returns "" — the anchor gate already
// guarantees kept findings land on a commentable line.
func (ci *ContentIndex) Anchor(path, side string, line int) string {
	m := ci.right
	if side == string(sideLeft) {
		m = ci.left
	}
	return m[key{path, line}]
}

// ContentsFor returns every diff line's content for (path, side), in
// unspecified order. Used by the delta review's "anchor gone" check: a prior
// finding is resolved when none of the current diff lines reproduce its
// fingerprint.
func (ci *ContentIndex) ContentsFor(path, side string) []string {
	m := ci.right
	if side == string(sideLeft) {
		m = ci.left
	}
	var out []string
	for k, v := range m {
		if k.path == path {
			out = append(out, v)
		}
	}
	return out
}

// sideLeft mirrors findings.SideLeft without importing findings (keeping this
// package a leaf). The value must stay in sync with the GitHub side vocabulary.
const sideLeft = "LEFT"
