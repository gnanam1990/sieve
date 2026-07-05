// Package diff parses git unified diffs into an exact line-anchor model.
//
// The anchor guarantee: for every Added and Context line, NewNum is the
// value the GitHub Reviews API accepts as "line" with side "RIGHT"; every
// Removed line carries OldNum for side "LEFT".
package diff

import (
	"encoding/json"
	"fmt"
)

// FileStatus classifies a file entry in a diff.
type FileStatus int

// File statuses, mirroring git's diff classification.
const (
	Added FileStatus = iota
	Modified
	Deleted
	Renamed
	Copied
	Binary
)

var statusNames = map[FileStatus]string{
	Added:    "added",
	Modified: "modified",
	Deleted:  "deleted",
	Renamed:  "renamed",
	Copied:   "copied",
	Binary:   "binary",
}

func (s FileStatus) String() string {
	if n, ok := statusNames[s]; ok {
		return n
	}
	return fmt.Sprintf("FileStatus(%d)", int(s))
}

// MarshalJSON encodes the status as its lowercase name so golden files and
// the dry-run output stay human-readable.
func (s FileStatus) MarshalJSON() ([]byte, error) {
	n, ok := statusNames[s]
	if !ok {
		return nil, fmt.Errorf("unknown FileStatus %d", int(s))
	}
	return json.Marshal(n)
}

// UnmarshalJSON decodes a lowercase status name.
func (s *FileStatus) UnmarshalJSON(b []byte) error {
	var n string
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	for k, v := range statusNames {
		if v == n {
			*s = k
			return nil
		}
	}
	return fmt.Errorf("unknown FileStatus %q", n)
}

// LineKind classifies a single diff line.
type LineKind int

// Line kinds. Named AddedLine/RemovedLine to avoid colliding with the
// Added/Deleted file statuses.
const (
	Context LineKind = iota
	AddedLine
	RemovedLine
)

var kindNames = map[LineKind]string{
	Context:     "context",
	AddedLine:   "added",
	RemovedLine: "removed",
}

func (k LineKind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return fmt.Sprintf("LineKind(%d)", int(k))
}

// MarshalJSON encodes the kind as its lowercase name.
func (k LineKind) MarshalJSON() ([]byte, error) {
	n, ok := kindNames[k]
	if !ok {
		return nil, fmt.Errorf("unknown LineKind %d", int(k))
	}
	return json.Marshal(n)
}

// UnmarshalJSON decodes a lowercase kind name.
func (k *LineKind) UnmarshalJSON(b []byte) error {
	var n string
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	for key, v := range kindNames {
		if v == n {
			*k = key
			return nil
		}
	}
	return fmt.Errorf("unknown LineKind %q", n)
}

// Line is one line of a hunk.
type Line struct {
	Kind    LineKind
	OldNum  int    // 0 when N/A (Added lines)
	NewNum  int    // 0 when N/A (Removed lines)
	Content string // raw, without the leading +/-/space marker
	NoEOF   bool   `json:",omitempty"` // followed by "\ No newline at end of file"
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Header             string // function context after the closing @@, may be empty
	Lines              []Line
}

// FileDiff is one file entry of a unified diff.
type FileDiff struct {
	OldPath, NewPath string
	Status           FileStatus
	Hunks            []Hunk
}
