// Package resultstore persists completed review results — the summary and
// every finding with its curation state — so a review survives navigating
// away or closing the session, and can be reopened later. Records live next
// to the run logs and raw transcripts in the state directory.
package resultstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// Store reads and writes review records in one directory. A nil Store is a
// no-op writer, so callers can save unconditionally.
type Store struct{ dir string }

// NewStore returns a store rooted at dir; the directory is created lazily
// on the first Save.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Record is one review's stored result. Findings carry their curation
// states, so re-saving after each accept/reject/edit keeps the record
// current with the screen.
type Record struct {
	IID     int64     `json:"iid"`
	Ref     string    `json:"ref"` // project!iid; IIDs alone collide across projects
	Title   string    `json:"title"`
	Started time.Time `json:"started"`
	// BaseSHA and HeadSHA are the MR diff refs the review ran against. They
	// key the record to a commit so a later run can review only what changed
	// since (incremental re-review); empty on records from before they were
	// stored.
	BaseSHA   string           `json:"base_sha,omitempty"`
	HeadSHA   string           `json:"head_sha,omitempty"`
	Summary   string           `json:"summary,omitempty"`
	Warnings  []string         `json:"warnings,omitempty"`
	SessionID string           `json:"session_id,omitempty"`
	CostUSD   float64          `json:"cost_usd,omitempty"`
	LogPath   string           `json:"log_path,omitempty"` // this run's progress log
	Findings  []review.Finding `json:"findings"`
}

// Path returns where Save writes rec: a file in the store directory keyed
// by IID and start time. Empty for a nil or unrooted store.
func (s *Store) Path(rec Record) string {
	if s == nil || s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, fmt.Sprintf("review-%d-%d.json", rec.IID, rec.Started.Unix()))
}

// Save writes the record, atomically replacing any earlier save of the same
// run: the file is keyed by IID and start time, so curation updates
// overwrite in place.
func (s *Store) Save(rec Record) error {
	if s == nil || s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := s.Path(rec)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads one stored record.
func (s *Store) Load(path string) (Record, error) {
	var rec Record
	data, err := os.ReadFile(path) //nolint:gosec // paths come from listing the store's own directory
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("decoding %s: %w", filepath.Base(path), err)
	}
	return rec, nil
}

// Latest returns the newest stored record for ref (the full record, not
// just its listing entry), or nil when the MR has no stored reviews.
func (s *Store) Latest(ref string) (*Record, error) {
	entries, err := s.List(ref)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	rec, err := s.Load(entries[0].Path)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// Entry describes one stored record, with enough of its content for a
// history list.
type Entry struct {
	Path     string
	Ref      string
	Title    string
	Started  time.Time
	LogPath  string
	Findings int
	Accepted int // accepted, published or fell back to a note
}

var nameRe = regexp.MustCompile(`^review-\d+-\d+\.json$`)

// List returns stored records, newest first. A non-empty ref restricts the
// result to that MR. Undecodable files are skipped, not fatal.
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
		if !nameRe.MatchString(de.Name()) {
			continue
		}
		path := filepath.Join(s.dir, de.Name())
		rec, err := s.Load(path)
		if err != nil || (ref != "" && rec.Ref != ref) {
			continue
		}
		e := Entry{
			Path:     path,
			Ref:      rec.Ref,
			Title:    rec.Title,
			Started:  rec.Started,
			LogPath:  rec.LogPath,
			Findings: len(rec.Findings),
		}
		for _, f := range rec.Findings {
			switch f.State {
			case review.StateAccepted, review.StatePublished, review.StateFellBack:
				e.Accepted++
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Started.Equal(out[j].Started) {
			return out[i].Started.After(out[j].Started)
		}
		return out[i].Path > out[j].Path
	})
	return out, nil
}

// LatestBlocking counts the findings in the MR's newest stored review that
// block a severity gate at min (see review.Finding.Blocking). Zero when no
// review is stored, so an unreviewed MR is never gated.
func (s *Store) LatestBlocking(ref string, min review.Severity) (int, error) {
	entries, err := s.List(ref)
	if err != nil || len(entries) == 0 {
		return 0, err
	}
	rec, err := s.Load(entries[0].Path)
	if err != nil {
		return 0, err
	}
	return review.CountBlocking(rec.Findings, min), nil
}
