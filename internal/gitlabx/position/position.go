// Package position maps review findings onto GitLab diff positions: the
// base/head/start SHAs plus old/new path and line GitLab requires for an
// inline discussion. Findings that cannot be anchored return ErrUnresolved
// so callers can fall back to a general note.
package position

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// ErrUnresolved means the finding's line is not part of any hunk in the MR
// diff; the comment can only be posted as a general note.
var ErrUnresolved = errors.New("line not present in the merge request diff")

// LineKind classifies one line of a parsed diff.
type LineKind int

const (
	Added LineKind = iota
	Removed
	Context
)

type lineInfo struct {
	kind LineKind
	// counterpart is the matching line number on the other side; only
	// meaningful for context lines.
	counterpart int
}

// FileIndex is a parsed file diff: lookups from new-side and old-side line
// numbers to their classification.
type FileIndex struct {
	OldPath string
	NewPath string
	byNew   map[int]lineInfo // added + context lines
	byOld   map[int]lineInfo // removed + context lines
}

// Index parses every file diff once so repeated resolutions are cheap.
func Index(diffs []gitlabx.FileDiff) []FileIndex {
	out := make([]FileIndex, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, parseFile(d))
	}
	return out
}

func parseFile(d gitlabx.FileDiff) FileIndex {
	fi := FileIndex{
		OldPath: d.OldPath,
		NewPath: d.NewPath,
		byNew:   map[int]lineInfo{},
		byOld:   map[int]lineInfo{},
	}
	oldLine, newLine := 0, 0
	inHunk := false
	for line := range strings.SplitSeq(d.Diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			o, n, ok := parseHunkHeader(line)
			if !ok {
				inHunk = false
				continue
			}
			oldLine, newLine = o, n
			inHunk = true
		case !inHunk || line == "":
			continue
		case strings.HasPrefix(line, "+"):
			fi.byNew[newLine] = lineInfo{kind: Added}
			newLine++
		case strings.HasPrefix(line, "-"):
			fi.byOld[oldLine] = lineInfo{kind: Removed}
			oldLine++
		case strings.HasPrefix(line, `\`): // "\ No newline at end of file"
			continue
		default:
			fi.byNew[newLine] = lineInfo{kind: Context, counterpart: oldLine}
			fi.byOld[oldLine] = lineInfo{kind: Context, counterpart: newLine}
			oldLine++
			newLine++
		}
	}
	return fi
}

// parseHunkHeader extracts the starting old and new line numbers from a
// header like "@@ -12,7 +12,9 @@ func foo() {".
func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	rest, found := strings.CutPrefix(line, "@@ ")
	if !found {
		return 0, 0, false
	}
	body, _, found := strings.Cut(rest, " @@")
	if !found {
		return 0, 0, false
	}
	oldPart, newPart, found := strings.Cut(body, " ")
	if !found || !strings.HasPrefix(oldPart, "-") || !strings.HasPrefix(newPart, "+") {
		return 0, 0, false
	}
	oldStart = hunkStart(oldPart[1:])
	newStart = hunkStart(newPart[1:])
	if oldStart == 0 && newStart == 0 {
		return 0, 0, false
	}
	// Zero-length ranges ("-0,0") still count lines from 1 in the other file.
	return max(oldStart, 1), max(newStart, 1), true
}

func hunkStart(s string) int {
	numStr, _, _ := strings.Cut(s, ",")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

// Resolve maps a finding location (file plus old/new line as reported by
// the reviewer) to a GitLab position. The file is matched against the new
// path first, then the old path (deleted/renamed files); paths on the
// returned position always come from the diff entry, never the finding.
func Resolve(file string, oldLine, newLine *int, index []FileIndex, refs gitlabx.DiffRefs) (*gitlabx.Position, error) {
	fi := findFile(file, index)
	if fi == nil {
		return nil, fmt.Errorf("%s: %w", file, ErrUnresolved)
	}

	pos := &gitlabx.Position{
		BaseSHA:  refs.BaseSHA,
		HeadSHA:  refs.HeadSHA,
		StartSHA: refs.StartSHA,
		OldPath:  fi.OldPath,
		NewPath:  fi.NewPath,
	}

	// Prefer the new-side anchor: that is where added and context lines live.
	if newLine != nil {
		if info, ok := fi.byNew[*newLine]; ok {
			switch info.kind {
			case Added:
				pos.NewLine = newLine
			case Context:
				// GitLab requires BOTH lines on unchanged lines.
				pos.NewLine = newLine
				old := info.counterpart
				pos.OldLine = &old
			}
			return pos, nil
		}
	}
	if oldLine != nil {
		if info, ok := fi.byOld[*oldLine]; ok {
			switch info.kind {
			case Removed:
				pos.OldLine = oldLine
			case Context:
				pos.OldLine = oldLine
				newL := info.counterpart
				pos.NewLine = &newL
			}
			return pos, nil
		}
	}
	return nil, fmt.Errorf("%s:%s: %w", file, lineDesc(oldLine, newLine), ErrUnresolved)
}

func findFile(file string, index []FileIndex) *FileIndex {
	for i := range index {
		if index[i].NewPath == file {
			return &index[i]
		}
	}
	for i := range index {
		if index[i].OldPath == file {
			return &index[i]
		}
	}
	return nil
}

func lineDesc(oldLine, newLine *int) string {
	switch {
	case newLine != nil:
		return strconv.Itoa(*newLine)
	case oldLine != nil:
		return "old:" + strconv.Itoa(*oldLine)
	default:
		return "?"
	}
}
