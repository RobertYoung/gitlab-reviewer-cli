// Package runlog persists the progress log of each review run — the same
// timestamped lines streamed to the review screen — so a run can be read
// back after its screen is gone. Logs live next to the raw stream
// transcripts in the state directory.
package runlog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const headerPrefix = "review of "

// Store writes and lists run logs in one directory. A nil Store (and the
// nil Log it hands out) is a no-op, so callers can log unconditionally.
type Store struct{ dir string }

// NewStore returns a store rooted at dir; the directory is created lazily
// on the first Start.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Log is one review run's open log file.
type Log struct {
	f       *os.File
	path    string
	started time.Time
}

// Start opens the log for one run and writes its header. Failures degrade
// to a nil (no-op) Log: a review must never abort because its log cannot
// be written.
func (s *Store) Start(iid int64, ref, title string) *Log {
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil
	}
	started := time.Now()
	path := filepath.Join(s.dir, fmt.Sprintf("review-%d-%d.log", iid, started.Unix()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is built from the state dir and numbers
	if err != nil {
		return nil
	}
	_, _ = fmt.Fprintf(f, "%s%s — %s\nstarted %s\n\n", headerPrefix, ref, title, started.Format(time.RFC3339))
	return &Log{f: f, path: path, started: started}
}

// Path returns the log file location, or "" for a no-op log.
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Append writes one progress line with the elapsed time, in the same
// format the review screen shows.
func (l *Log) Append(text string) {
	if l == nil || l.f == nil {
		return
	}
	_, _ = fmt.Fprintf(l.f, "%6s  %s\n", time.Since(l.started).Round(time.Second), text)
}

// Finish writes the outcome footer and closes the file.
func (l *Log) Finish(outcome string) {
	if l == nil || l.f == nil {
		return
	}
	_, _ = fmt.Fprintf(l.f, "\n%s after %s\n", outcome, time.Since(l.started).Round(time.Second))
	_ = l.f.Close()
	l.f = nil
}

// Entry describes one stored run log.
type Entry struct {
	Path    string
	Ref     string
	Title   string
	Started time.Time
}

var nameRe = regexp.MustCompile(`^review-\d+-(\d+)\.log$`)

// List returns stored run logs, newest first. A non-empty ref restricts
// the result to that MR; the ref (project!iid) is matched from the file
// header because IIDs alone collide across projects.
func (s *Store) List(ref string) ([]Entry, error) {
	if s == nil || s.dir == "" {
		return nil, nil
	}
	dirents, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, de := range dirents {
		m := nameRe.FindStringSubmatch(de.Name())
		if m == nil {
			continue
		}
		path := filepath.Join(s.dir, de.Name())
		entryRef, title, ok := readHeader(path)
		if !ok || (ref != "" && entryRef != ref) {
			continue
		}
		ts, _ := strconv.ParseInt(m[1], 10, 64)
		out = append(out, Entry{Path: path, Ref: entryRef, Title: title, Started: time.Unix(ts, 0)})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.After(out[j].Started)
		}
		return out[i].Path > out[j].Path
	})
	return out, nil
}

// readHeader parses "review of <ref> — <title>" from the first line.
func readHeader(path string) (ref, title string, ok bool) {
	f, err := os.Open(path) //nolint:gosec // paths come from listing the store's own directory
	if err != nil {
		return "", "", false
	}
	defer f.Close() //nolint:errcheck // read-only
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	line, _, _ := strings.Cut(string(buf[:n]), "\n")
	rest, found := strings.CutPrefix(line, headerPrefix)
	if !found {
		return "", "", false
	}
	ref, title, _ = strings.Cut(rest, " — ")
	return ref, title, true
}
