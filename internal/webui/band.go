package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review/resultstore"
)

// mrBand is the header every MR-scoped page renders above its content: the
// MR's identity, where its review stands as a four-step pipeline (AI review,
// triage, publish, approve) whose next step is the one primary action, and
// the page tabs.
type mrBand struct {
	Nav       mrNav
	Detail    *gitlabx.MRDetail // nil when GitLab could not be reached
	Ref       string
	Title     string
	Tab       string // overview, changes, findings, reviews or chat
	Runs      int    // past reviews, for that tab's counter
	Review    *bandReview
	Comments  int // accepted manual comments awaiting publication
	Approvals *gitlabx.Approvals
	Gate      bandGate
	RunURL    string // a review of this MR still in flight
	Quiet     bool   // the page is itself a pipeline step: no competing action
	Steps     []bandStep
	Next      *bandAction
	BackURL   string // where the approve form returns to
}

// bandReview summarises the stored review the band reports on: the page's
// own record where it has one, otherwise the MR's newest.
type bandReview struct {
	Name         string
	FirstPending string // the finding triage resumes at
	When         time.Time
	Findings     int
	Pending      int
	Accepted     int // accepted and not yet posted
	Rejected     int
	Published    int
	FindingsURL  string
	PublishURL   string
}

// bandGate is the severity gate's verdict on the latest review: Blocking
// findings at or above Severity, and whether approval is refused outright.
type bandGate struct {
	Blocking int
	Severity string
	Blocked  bool
}

type bandStep struct {
	Key    string // review, triage, publish, approve
	Label  string
	Detail string
	URL    string
	State  string // done, current or todo
}

type bandAction struct {
	Label   string
	URL     string
	Icon    string
	Go      bool // the green run-review styling
	Approve bool // rendered as the approve form rather than a link
}

// bandInput is what a handler already knows when it builds the band, so
// nothing is fetched twice.
type bandInput struct {
	Inst    string
	Project string
	IID     int64
	Tab     string
	Detail  *gitlabx.MRDetail // fetched here when nil
	Record  string            // the page's stored review; "" means the newest
	Ref     string            // fallbacks when GitLab is unreachable
	Title   string
	BackURL string
}

// band assembles the MR header from GitLab (detail, approvals) and the local
// stores. Everything but the MR's identity is best-effort: a failed lookup
// leaves its step undecided rather than failing the page.
func (s *Server) band(r *http.Request, d *Deps, in bandInput) *mrBand {
	b := &mrBand{
		Nav:     newMRNav(in.Inst, in.Project, in.IID),
		Detail:  in.Detail,
		Ref:     in.Ref,
		Title:   in.Title,
		Tab:     in.Tab,
		BackURL: in.BackURL,
	}
	if b.Ref == "" {
		b.Ref = fmt.Sprintf("%s!%d", in.Project, in.IID)
	}
	if b.Detail == nil {
		b.Detail, _ = d.Svc.GetMergeRequest(r.Context(), parseProject(in.Project), in.IID)
	}
	if b.Detail != nil {
		b.Ref, b.Title = b.Detail.Ref(), b.Detail.Title
		b.Approvals, _ = d.Svc.GetApprovals(r.Context(), b.Detail.Project(), in.IID)
		if b.BackURL == "" {
			b.BackURL = b.Nav.DetailURL
		}
	}
	b.Runs = len(s.historyRows(d, in.Inst, in.Project, in.IID, b.Ref))
	b.Review = s.bandReview(d, in, b.Ref)
	b.Comments = len(s.comments.accepted(mrKey(in.Inst, in.Project, in.IID)))
	if run := s.runs.active(in.Inst, in.Project, in.IID); run != nil {
		b.RunURL = instPath(in.Inst, "/run/"+run.ID)
	}
	if b.Detail != nil {
		if gate := d.cfgFor(b.Detail.ProjectPath).Gate; gate.Enabled() && gate.Approvals != "off" {
			if n, err := d.Results.LatestBlocking(b.Ref, review.Severity(gate.MinSeverity)); err == nil && n > 0 {
				b.Gate = bandGate{Blocking: n, Severity: gate.MinSeverity, Blocked: gate.Approvals == "block"}
			}
		}
	}
	b.plan()
	return b
}

func (s *Server) bandReview(d *Deps, in bandInput, ref string) *bandReview {
	name := in.Record
	if name == "" {
		entries, err := d.Results.List(ref)
		if err != nil || len(entries) == 0 {
			return nil
		}
		name = filepath.Base(entries[0].Path) // newest first
	}
	path, err := s.safeStoreFile(name, ".json")
	if err != nil {
		return nil
	}
	rec, err := d.Results.Load(path)
	if err != nil {
		return nil
	}
	return summariseReview(rec, name, in.Inst, in.Project, in.IID)
}

func summariseReview(rec resultstore.Record, name, inst, project string, iid int64) *bandReview {
	q := url.Values{"record": {name}}
	br := &bandReview{
		Name:        name,
		When:        rec.Started,
		Findings:    len(rec.Findings),
		FindingsURL: mrURL(inst, "/mr/findings", project, iid, q),
		PublishURL:  mrURL(inst, "/mr/publish", project, iid, q),
	}
	for _, f := range rec.Findings {
		switch f.State {
		case review.StatePending:
			br.Pending++
			if br.FirstPending == "" {
				br.FirstPending = f.ID
			}
		case review.StateAccepted:
			br.Accepted++
		case review.StateRejected:
			br.Rejected++
		case review.StatePublished, review.StateFellBack:
			br.Published++
		}
	}
	return br
}

// plan derives the pipeline steps and the single next action from what the
// band knows. The first unfinished step is the current one.
func (b *mrBand) plan() {
	rv := b.Review
	review := bandStep{Key: "review", Label: "AI review", Detail: "Not run yet", URL: b.Nav.ReviewFormURL}
	triage := bandStep{Key: "triage", Label: "Triage", Detail: "Waiting for a review", URL: b.Nav.HistoryURL}
	publish := bandStep{Key: "publish", Label: "Publish", Detail: "Nothing accepted yet", URL: b.Nav.HistoryURL}
	approve := bandStep{Key: "approve", Label: "Approve", Detail: "No approval data", URL: b.Nav.DetailURL + "#approval"}

	switch {
	case b.RunURL != "":
		review.Detail, review.URL = "Running now", b.RunURL
	case rv != nil:
		review.State, review.URL = "done", b.Nav.HistoryURL
		review.Detail = plural(rv.Findings, "finding") + " · " + relTime(rv.When)
	}
	if rv != nil {
		triage.URL, publish.URL = rv.FindingsURL, rv.FindingsURL
		triage.Detail = "Nothing to triage"
		if rv.Pending > 0 {
			triage.Detail = fmt.Sprintf("%d of %d still pending", rv.Pending, rv.Findings)
		} else {
			triage.State = "done"
			if rv.Findings > 0 {
				triage.Detail = "All " + plural(rv.Findings, "finding") + " triaged"
			}
		}
		switch {
		case rv.Accepted > 0:
			publish.Detail, publish.URL = fmt.Sprintf("%d accepted, ready to post", rv.Accepted), rv.PublishURL
		case rv.Published > 0:
			publish.State, publish.Detail = "done", fmt.Sprintf("%d posted to GitLab", rv.Published)
		case rv.Pending == 0:
			publish.State, publish.Detail = "done", "Nothing to post"
		}
	}
	if b.Comments > 0 && (rv == nil || rv.Accepted == 0) {
		publish.State, publish.URL = "", b.Nav.PublishURL
		publish.Detail = plural(b.Comments, "comment") + " ready to post"
	}
	if a := b.Approvals; a != nil {
		switch {
		case a.UserHasApproved:
			approve.State, approve.Detail = "done", "Approved by you"
		case a.Approved:
			approve.State, approve.Detail = "done", "Approved"
		case a.ApprovalsRequired > 0:
			approve.Detail = fmt.Sprintf("%d of %d approvals left", a.ApprovalsLeft, a.ApprovalsRequired)
		default:
			approve.Detail = "Not approved yet"
		}
		if b.Gate.Blocking > 0 && approve.State != "done" {
			approve.Detail = plural(b.Gate.Blocking, "blocking finding")
		}
	}

	b.Steps = []bandStep{review, triage, publish, approve}
	for i := range b.Steps {
		if b.Steps[i].State == "" {
			b.Steps[i].State = "current"
			for j := i + 1; j < len(b.Steps); j++ {
				if b.Steps[j].State == "" {
					b.Steps[j].State = "todo"
				}
			}
			break
		}
	}

	switch {
	case b.RunURL != "":
		b.Next = &bandAction{Label: "View progress", URL: b.RunURL, Icon: "sync"}
	case rv == nil:
		b.Next = &bandAction{Label: "Run AI review", URL: b.Nav.ReviewFormURL, Icon: "play", Go: true}
	case rv.Pending > 0:
		b.Next = &bandAction{Label: "Continue triage", URL: rv.FindingsURL + "#f-" + url.QueryEscape(rv.FirstPending), Icon: "arrow-right"}
	case rv.Accepted > 0:
		b.Next = &bandAction{Label: fmt.Sprintf("Publish %d accepted", rv.Accepted), URL: rv.PublishURL, Icon: "send"}
	case b.Comments > 0:
		b.Next = &bandAction{Label: "Publish " + plural(b.Comments, "comment"), URL: b.Nav.PublishURL, Icon: "send"}
	case b.Approvals != nil && b.Approvals.UserCanApprove && !b.Approvals.UserHasApproved && !b.Gate.Blocked && b.Detail != nil:
		label := "Approve"
		if b.Gate.Blocking > 0 {
			label = "Approve anyway"
		}
		b.Next = &bandAction{Label: label, Icon: "check", Approve: true}
	}
}

// historyRows lists an MR's past review runs, newest first: stored records
// lead, and logs whose run left no record get log-only rows.
func (s *Server) historyRows(d *Deps, inst, project string, iid int64, ref string) []historyRow {
	records, _ := d.Results.List(ref)
	logs, _ := d.Logs.List(ref)
	logURL := func(path string) string {
		if path == "" {
			return ""
		}
		return mrURL(inst, "/mr/log", project, iid, url.Values{"name": {filepath.Base(path)}})
	}
	covered := map[string]bool{}
	var rows []historyRow
	for _, rec := range records {
		covered[rec.LogPath] = true
		rows = append(rows, historyRow{
			Started:     rec.Started,
			Title:       rec.Title,
			Findings:    rec.Findings,
			Accepted:    rec.Accepted,
			Pending:     rec.Pending,
			FindingsURL: mrURL(inst, "/mr/findings", project, iid, url.Values{"record": {filepath.Base(rec.Path)}}),
			LogURL:      logURL(rec.LogPath),
		})
	}
	for _, l := range logs {
		if covered[l.Path] {
			continue
		}
		rows = append(rows, historyRow{Started: l.Started, Title: l.Title, LogURL: logURL(l.Path)})
	}
	sortHistory(rows)
	return rows
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
