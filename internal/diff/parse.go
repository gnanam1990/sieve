package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: (.*))?$`)

// Parse parses a git unified diff into FileDiffs. Content bytes are
// preserved exactly (CRLF intact); only the trailing LF and the leading
// +/-/space marker are stripped from each line.
func Parse(data []byte) ([]FileDiff, error) {
	lines := splitLines(data)
	var files []FileDiff
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "diff --git ") {
			// Tolerate preamble (e.g. commit headers from `git show`).
			i++
			continue
		}
		fd, next, err := parseFile(lines, i)
		if err != nil {
			return nil, err
		}
		files = append(files, fd)
		i = next
	}
	return files, nil
}

func parseFile(lines []string, i int) (FileDiff, int, error) {
	gitOld, gitNew := parseGitHeaderPaths(lines[i])
	i++

	var fd FileDiff
	var isNew, isDeleted, isRenamed, isCopied, isBinary bool
	oldPath, newPath := gitOld, gitNew

	for i < len(lines) && !strings.HasPrefix(lines[i], "diff --git ") {
		l := lines[i]
		switch {
		case strings.HasPrefix(l, "new file mode "):
			isNew = true
		case strings.HasPrefix(l, "deleted file mode "):
			isDeleted = true
		case strings.HasPrefix(l, "rename from "):
			isRenamed = true
			oldPath = unquotePath(strings.TrimPrefix(l, "rename from "))
		case strings.HasPrefix(l, "rename to "):
			isRenamed = true
			newPath = unquotePath(strings.TrimPrefix(l, "rename to "))
		case strings.HasPrefix(l, "copy from "):
			isCopied = true
			oldPath = unquotePath(strings.TrimPrefix(l, "copy from "))
		case strings.HasPrefix(l, "copy to "):
			isCopied = true
			newPath = unquotePath(strings.TrimPrefix(l, "copy to "))
		case strings.HasPrefix(l, "Binary files "), l == "GIT binary patch":
			isBinary = true
		case strings.HasPrefix(l, "--- "):
			oldPath = stripPathPrefix(l[4:], "a/")
		case strings.HasPrefix(l, "+++ "):
			newPath = stripPathPrefix(l[4:], "b/")
		case strings.HasPrefix(l, "@@ "):
			h, next, err := parseHunk(lines, i)
			if err != nil {
				return fd, i, err
			}
			fd.Hunks = append(fd.Hunks, h)
			i = next
			continue
		default:
			// Extended headers we don't model (mode, index, similarity),
			// binary patch payload, or unknown trailer: skip.
		}
		i++
	}

	switch {
	case isBinary:
		fd.Status = Binary
		fd.Hunks = nil
	case isNew:
		fd.Status = Added
		oldPath = ""
	case isDeleted:
		fd.Status = Deleted
		newPath = ""
	case isRenamed:
		fd.Status = Renamed
	case isCopied:
		fd.Status = Copied
	default:
		fd.Status = Modified
	}
	fd.OldPath, fd.NewPath = oldPath, newPath
	return fd, i, nil
}

func parseHunk(lines []string, i int) (Hunk, int, error) {
	m := hunkHeaderRe.FindStringSubmatch(lines[i])
	if m == nil {
		return Hunk{}, i, fmt.Errorf("line %d: malformed hunk header %q", i+1, lines[i])
	}
	h := Hunk{
		OldStart: atoi(m[1]),
		OldLines: atoiDefault(m[2], 1),
		NewStart: atoi(m[3]),
		NewLines: atoiDefault(m[4], 1),
		Header:   m[5],
	}
	i++

	oldNum, newNum := h.OldStart, h.NewStart
	oldRemain, newRemain := h.OldLines, h.NewLines
	for oldRemain > 0 || newRemain > 0 {
		if i >= len(lines) {
			return h, i, fmt.Errorf("unexpected end of diff inside hunk starting @@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		}
		l := lines[i]
		switch {
		case strings.HasPrefix(l, `\`):
			markNoEOF(&h)
		case strings.HasPrefix(l, "+"):
			h.Lines = append(h.Lines, Line{Kind: AddedLine, NewNum: newNum, Content: l[1:]})
			newNum++
			newRemain--
		case strings.HasPrefix(l, "-"):
			h.Lines = append(h.Lines, Line{Kind: RemovedLine, OldNum: oldNum, Content: l[1:]})
			oldNum++
			oldRemain--
		case strings.HasPrefix(l, " "), l == "":
			// Some tools strip the trailing space of empty context lines.
			content := ""
			if l != "" {
				content = l[1:]
			}
			h.Lines = append(h.Lines, Line{Kind: Context, OldNum: oldNum, NewNum: newNum, Content: content})
			oldNum++
			newNum++
			oldRemain--
			newRemain--
		default:
			return h, i, fmt.Errorf("line %d: unexpected line inside hunk: %q", i+1, l)
		}
		i++
	}
	// A trailing "\ No newline at end of file" can follow the final line.
	if i < len(lines) && strings.HasPrefix(lines[i], `\`) {
		markNoEOF(&h)
		i++
	}
	return h, i, nil
}

func markNoEOF(h *Hunk) {
	if len(h.Lines) > 0 {
		h.Lines[len(h.Lines)-1].NoEOF = true
	}
}

// parseGitHeaderPaths extracts old/new paths from a "diff --git a/X b/Y"
// line. This is only a fallback for entries with no ---/+++ or rename/copy
// headers (binary, mode-only); paths containing " b/" are ambiguous here,
// so prefer the split where both halves match (the common old==new case).
func parseGitHeaderPaths(l string) (string, string) {
	rest := strings.TrimPrefix(l, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		// Quoted paths: split at the quote boundary.
		if end := strings.Index(rest[1:], `" `); end >= 0 {
			return unquotePath(rest[:end+2]), unquotePath(rest[end+3:])
		}
	}
	var firstOld, firstNew string
	for idx := 0; idx+3 <= len(rest); idx++ {
		if !strings.HasPrefix(rest[idx:], " b/") {
			continue
		}
		o, n := rest[:idx], rest[idx+1:]
		if !strings.HasPrefix(o, "a/") {
			continue
		}
		o, n = o[2:], n[2:]
		if o == n {
			return o, n
		}
		if firstOld == "" {
			firstOld, firstNew = o, n
		}
	}
	return firstOld, firstNew
}

// stripPathPrefix handles a ---/+++ path: strips the a// b/ prefix, the
// optional trailing tab git emits for paths with trailing whitespace, and
// maps /dev/null to "".
func stripPathPrefix(p, prefix string) string {
	p = strings.TrimSuffix(p, "\t")
	p = unquotePath(p)
	if p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, prefix)
}

// unquotePath undoes git's C-style quoting of paths with special characters.
func unquotePath(p string) string {
	if strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// splitLines splits on LF only, preserving CR so CRLF content stays
// byte-exact. A final line without a trailing LF is kept.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	return atoi(s)
}
