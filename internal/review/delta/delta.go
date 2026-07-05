// Package delta supports incremental re-review: given the diff between the
// last reviewed head and the current one, it maps finding anchors from the
// old head's line numbers to the new head's, so findings on unchanged code
// carry forward (with their curation state) and findings whose anchor lines
// were changed or removed are dropped.
package delta

import (
	"sort"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// Mapper answers line-number questions about one set of file diffs. For a
// delta diff (old head → new head) the old side is the previously reviewed
// head and the new side is the current one.
type Mapper struct {
	files []*fileDelta
}

type fileDelta struct {
	oldPath string
	newPath string
	deleted bool
	// context maps old-side line numbers of in-hunk context lines to their
	// new-side counterparts; removed records old-side lines that no longer
	// exist on the new side.
	context map[int]int
	removed map[int]bool
	hunks   []hunkRange
}

// hunkRange is one hunk's old-side extent plus the cumulative old→new
// offset that applies to lines after it.
type hunkRange struct {
	oldStart, oldEnd int // old side, end exclusive
	offsetAfter      int
}

// NewMapper parses diffs once so repeated lookups are cheap.
func NewMapper(diffs []gitlabx.FileDiff) *Mapper {
	m := &Mapper{}
	for _, d := range diffs {
		m.files = append(m.files, parseFile(d))
	}
	return m
}

func parseFile(d gitlabx.FileDiff) *fileDelta {
	fd := &fileDelta{
		oldPath: d.OldPath,
		newPath: d.NewPath,
		deleted: d.DeletedFile,
		context: map[int]int{},
		removed: map[int]bool{},
	}
	oldLine, newLine := 0, 0
	offset := 0
	var cur *hunkRange
	endHunk := func() {
		if cur != nil {
			cur.oldEnd = oldLine
			cur.offsetAfter = offset
			fd.hunks = append(fd.hunks, *cur)
			cur = nil
		}
	}
	for line := range strings.SplitSeq(d.Diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			endHunk()
			o, oldCount, n, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			oldLine, newLine = o, n
			start := o
			if oldCount == 0 {
				// A pure insertion "-l,0" sits after old line l: line l itself
				// is unaffected, the shift starts at l+1.
				start = o + 1
				oldLine = start
			}
			cur = &hunkRange{oldStart: start}
		case cur == nil || line == "":
			continue
		case strings.HasPrefix(line, "+"):
			offset++
			newLine++
		case strings.HasPrefix(line, "-"):
			fd.removed[oldLine] = true
			offset--
			oldLine++
		case strings.HasPrefix(line, `\`): // "\ No newline at end of file"
			continue
		default:
			fd.context[oldLine] = newLine
			oldLine++
			newLine++
		}
	}
	endHunk()
	sort.Slice(fd.hunks, func(i, j int) bool { return fd.hunks[i].oldStart < fd.hunks[j].oldStart })
	return fd
}

// parseHunkHeader extracts the old range and the new starting line from a
// header like "@@ -12,7 +12,9 @@ func foo() {". The old count distinguishes
// pure insertions ("-l,0"), whose ranges sit between old lines.
func parseHunkHeader(line string) (oldStart, oldCount, newStart int, ok bool) {
	rest, found := strings.CutPrefix(line, "@@ ")
	if !found {
		return 0, 0, 0, false
	}
	body, _, found := strings.Cut(rest, " @@")
	if !found {
		return 0, 0, 0, false
	}
	oldPart, newPart, found := strings.Cut(body, " ")
	if !found || !strings.HasPrefix(oldPart, "-") || !strings.HasPrefix(newPart, "+") {
		return 0, 0, 0, false
	}
	oldStart, oldCount = hunkRangeOf(oldPart[1:])
	newStart, _ = hunkRangeOf(newPart[1:])
	if oldStart == 0 && newStart == 0 {
		return 0, 0, 0, false
	}
	// Zero-length ranges ("-0,0") still count lines from 1 in the other file.
	return max(oldStart, 1), oldCount, max(newStart, 1), true
}

// hunkRangeOf parses "start[,count]"; an omitted count means one line.
func hunkRangeOf(s string) (start, count int) {
	startStr, countStr, hasCount := strings.Cut(s, ",")
	start, err := strconv.Atoi(startStr)
	if err != nil {
		return 0, 0
	}
	count = 1
	if hasCount {
		if n, err := strconv.Atoi(countStr); err == nil {
			count = n
		}
	}
	return start, count
}

// find matches a finding's file path against the parsed diffs: new paths
// first, then old paths (deleted files are reported under their old path).
func (m *Mapper) find(file string) *fileDelta {
	for _, fd := range m.files {
		if fd.newPath == file {
			return fd
		}
	}
	for _, fd := range m.files {
		if fd.oldPath == file {
			return fd
		}
	}
	return nil
}

// Touches reports whether the diffs change the named file at all.
func (m *Mapper) Touches(file string) bool { return m.find(file) != nil }

// MapLine maps an old-side line of file to its new-side location. A file the
// diffs do not touch maps to itself. ok is false when the line (or the whole
// file) was changed or removed, i.e. a finding anchored there is stale.
func (m *Mapper) MapLine(file string, line int) (newFile string, newLine int, ok bool) {
	fd := m.find(file)
	if fd == nil {
		return file, line, true
	}
	if fd.deleted || fd.removed[line] {
		return "", 0, false
	}
	if n, inHunk := fd.context[line]; inHunk {
		return fd.newPath, n, true
	}
	// Outside every hunk: shift by the cumulative offset of the hunks above.
	offset := 0
	for _, h := range fd.hunks {
		if line >= h.oldEnd {
			offset = h.offsetAfter
			continue
		}
		if line >= h.oldStart {
			// Inside a hunk's old range but neither context nor removed can
			// only happen on a malformed diff; treat the anchor as stale.
			return "", 0, false
		}
		break
	}
	return fd.newPath, line + offset, true
}

// RemovedLine reports whether the diffs show the old-side line of file as
// removed. Used against the MR diff (base → head) to check that a finding
// anchored to a removed line is still about a removed line.
func (m *Mapper) RemovedLine(file string, line int) bool {
	fd := m.find(file)
	return fd != nil && (fd.deleted || fd.removed[line])
}

// CarryForward splits the previous review's findings into those still valid
// at the new head (with anchors remapped and curation state preserved) and
// those dropped because the code they were anchored to changed. deltaDiffs
// is the old-head→new-head comparison; mrDiffs is the MR's current diff
// (base→new head), used to validate findings anchored to removed lines. The
// caller guarantees the MR base did not move (a rebase falls back to a full
// review instead).
func CarryForward(prev []review.Finding, deltaDiffs, mrDiffs []gitlabx.FileDiff) (kept, dropped []review.Finding) {
	dm := NewMapper(deltaDiffs)
	var mm *Mapper // lazily parsed; only needed for removed-line findings
	for _, f := range prev {
		switch {
		case f.File == "":
			// MR-level notes and manual comments have no anchor to go stale.
			kept = append(kept, f)

		case f.Line.NewLine != nil:
			file, line, ok := dm.MapLine(f.File, *f.Line.NewLine)
			if !ok {
				dropped = append(dropped, f)
				continue
			}
			f.File = file
			f.Line.NewLine = &line
			// The old-side counterpart (context-line anchors) references the
			// MR base, which has not moved; leave it as is.
			kept = append(kept, f)

		case f.Line.OldLine != nil:
			// Anchored to a removed line: old-side numbers reference the MR
			// base, so the delta cannot map them. If the delta leaves the file
			// alone the finding is untouched; otherwise it survives only if
			// the MR diff still removes that line.
			if !dm.Touches(f.File) {
				kept = append(kept, f)
				continue
			}
			if mm == nil {
				mm = NewMapper(mrDiffs)
			}
			if mm.RemovedLine(f.File, *f.Line.OldLine) {
				kept = append(kept, f)
			} else {
				dropped = append(dropped, f)
			}

		default:
			// File-level finding with no line: follows the file.
			file, _, ok := dm.MapLine(f.File, 0)
			if !ok {
				dropped = append(dropped, f)
				continue
			}
			f.File = file
			kept = append(kept, f)
		}
	}
	return kept, dropped
}
