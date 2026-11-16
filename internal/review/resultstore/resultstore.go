// Package resultstore persists completed review results — the summary and
// every finding with its curation state — so a review survives navigating
// away or closing the session, and can be reopened later. Records live next
// to the run logs and raw transcripts in the state directory.
package resultstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/config"
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
	Path    string
	Ref     string
	Title   string
	Started time.Time
	LogPath string
	Counts
}

// Counts tallies a record's findings by curation state.
type Counts struct {
	Findings  int `json:"findings"`
	Accepted  int `json:"accepted"` // accepted, published or fell back to a note
	Rejected  int `json:"rejected"`
	Pending   int `json:"pending"`   // still awaiting triage
	Published int `json:"published"` // posted to GitLab, inline or as a fallback note
}

func CountFindings(findings []review.Finding) Counts {
	c := Counts{Findings: len(findings)}
	for _, f := range findings {
		switch f.State {
		case review.StateAccepted:
			c.Accepted++
		case review.StatePublished, review.StateFellBack:
			c.Accepted++
			c.Published++
		case review.StatePending:
			c.Pending++
		case review.StateRejected:
			c.Rejected++
		}
	}
	return c
}

// Stage names where a review stands: triage while findings are pending,
// publish while accepted ones wait to be posted, then published (or clean
// when nothing was found).
func (c Counts) Stage() string {
	switch {
	case c.Findings == 0:
		return "clean"
	case c.Pending > 0:
		return "triage"
	case c.Accepted > c.Published:
		return "publish"
	case c.Published > 0:
		return "published"
	default:
		return "triaged"
	}
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
		out = append(out, Entry{
			Path:    path,
			Ref:     rec.Ref,
			Title:   rec.Title,
			Started: rec.Started,
			LogPath: rec.LogPath,
			Counts:  CountFindings(rec.Findings),
		})
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

// LatestByRef returns the newest stored entry per MR ref, from one scan of
// the store.
func (s *Store) LatestByRef() (map[string]Entry, error) {
	entries, err := s.List("")
	latest := map[string]Entry{}
	for _, e := range entries {
		if _, ok := latest[e.Ref]; !ok {
			latest[e.Ref] = e
		}
	}
	return latest, err
}

// SafeFile validates a user-supplied record or log reference: it must be a
// bare review-*<ext> file name, resolved inside dir, so a reference can
// never reach outside the reviews directory.
func SafeFile(dir, name, ext string) (string, error) {
	if name == "" || name != filepath.Base(name) || !strings.HasPrefix(name, "review-") || !strings.HasSuffix(name, ext) {
		return "", fmt.Errorf("invalid file reference %q", name)
	}
	return filepath.Join(dir, name), nil
}

// Resolve maps a record name, as listed by List (the base name of an
// entry's Path), to its path inside the store.
func (s *Store) Resolve(name string) (string, error) {
	if s == nil {
		return "", errors.New("no result store")
	}
	return SafeFile(s.dir, name, ".json")
}

// Triage applies one curation action to the record's findings: accept or
// reject the finding with id, accept-all (every pending finding becomes
// accepted), or edit (replace the finding's body with the trimmed body).
// It fails on an unknown action, an id that matches no finding, or an empty
// edit body, leaving the record unchanged.
func (r *Record) Triage(action, id, body string) error {
	if action == "accept-all" {
		for i := range r.Findings {
			if r.Findings[i].State == review.StatePending {
				r.Findings[i].State = review.StateAccepted
			}
		}
		return nil
	}
	f := r.Finding(id)
	if f == nil {
		return fmt.Errorf("no finding with id %q in this review", id)
	}
	switch action {
	case "accept":
		f.State = review.StateAccepted
	case "reject":
		f.State = review.StateRejected
	case "edit":
		body = strings.TrimSpace(body)
		if body == "" {
			return errors.New("the edited body is empty")
		}
		f.Body = body
	default:
		return fmt.Errorf("unknown triage action %q", action)
	}
	return nil
}

// SetState records a publish outcome on every finding with id.
func (r *Record) SetState(id string, state review.FindingState) {
	for i := range r.Findings {
		if r.Findings[i].ID == id {
			r.Findings[i].State = state
		}
	}
}

func (r *Record) Finding(id string) *review.Finding {
	for i := range r.Findings {
		if r.Findings[i].ID == id {
			return &r.Findings[i]
		}
	}
	return nil
}

// ApprovalBlockedError reports an approval refused by a block-mode gate.
type ApprovalBlockedError struct {
	Blocking    int
	MinSeverity string
}

func (e *ApprovalBlockedError) Error() string {
	return fmt.Sprintf("approval blocked: %d finding(s) at or above %s in the last review (gate.approvals: block)", e.Blocking, e.MinSeverity)
}

// CheckApproval enforces a block-mode severity gate on approving ref: an
// *ApprovalBlockedError when the MR's newest stored review still has
// blocking findings, nil otherwise. Warn and off modes never block, and an
// unreadable store does not either, matching the frontends' behaviour.
func (s *Store) CheckApproval(gate config.Gate, ref string) error {
	if !gate.Enabled() || gate.Approvals != "block" {
		return nil
	}
	if n, err := s.LatestBlocking(ref, review.Severity(gate.MinSeverity)); err == nil && n > 0 {
		return &ApprovalBlockedError{Blocking: n, MinSeverity: gate.MinSeverity}
	}
	return nil
}
