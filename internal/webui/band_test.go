package webui

import (
	"net/url"
	"strings"
	"testing"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

func TestBandPlan(t *testing.T) {
	nav := newMRNav("default", "group/app", 5)
	review := func(pending, accepted, rejected, published int) *bandReview {
		return &bandReview{
			Findings: pending + accepted + rejected + published,
			Pending:  pending, Accepted: accepted, Rejected: rejected, Published: published,
			FindingsURL: "/findings", PublishURL: "/publish", FirstPending: "f2",
		}
	}
	canApprove := &gitlabx.Approvals{UserCanApprove: true}
	cases := []struct {
		name      string
		band      mrBand
		next      string
		current   string // the highlighted step, "" when all are done
		approveUI bool
	}{
		{"no review yet", mrBand{}, "Run AI review", "review", false},
		{"review running", mrBand{RunURL: "/run/r1"}, "View progress", "review", false},
		{"findings pending", mrBand{Review: review(2, 1, 1, 0)}, "Continue triage", "triage", false},
		{"accepted to post", mrBand{Review: review(0, 2, 1, 0)}, "Publish 2 accepted", "publish", false},
		{"manual comments to post", mrBand{Review: review(0, 0, 1, 0), Comments: 1}, "Publish 1 comment", "publish", false},
		{"posted, approvable", mrBand{Review: review(0, 0, 0, 2), Approvals: canApprove, Detail: &gitlabx.MRDetail{}}, "Approve", "approve", true},
		{"gate warns", mrBand{Review: review(0, 0, 0, 2), Approvals: canApprove, Detail: &gitlabx.MRDetail{}, Gate: bandGate{Blocking: 1}}, "Approve anyway", "approve", true},
		{"gate blocks", mrBand{Review: review(0, 0, 0, 2), Approvals: canApprove, Detail: &gitlabx.MRDetail{}, Gate: bandGate{Blocking: 1, Blocked: true}}, "", "approve", false},
		{"approved by you", mrBand{Review: review(0, 0, 0, 2), Approvals: &gitlabx.Approvals{UserCanApprove: true, UserHasApproved: true}, Detail: &gitlabx.MRDetail{}}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.band
			b.Nav = nav
			b.plan()
			next := ""
			if b.Next != nil {
				next = b.Next.Label
				if b.Next.Approve != tc.approveUI {
					t.Errorf("approve form = %v, want %v", b.Next.Approve, tc.approveUI)
				}
			}
			if next != tc.next {
				t.Errorf("next action = %q, want %q", next, tc.next)
			}
			if next == "Continue triage" && b.Next.URL != "/findings#f-f2" {
				t.Errorf("triage should resume at the first pending finding, got %q", b.Next.URL)
			}
			current := ""
			for _, s := range b.Steps {
				if s.State == "current" {
					current = s.Key
				}
			}
			if current != tc.current {
				t.Errorf("current step = %q, want %q (steps %+v)", current, tc.current, b.Steps)
			}
		})
	}
}

func TestReviewStage(t *testing.T) {
	for want, info := range map[string]mrReviewedInfo{
		"clean":     {},
		"triage":    {Findings: 3, Pending: 1, Accepted: 1},
		"publish":   {Findings: 2, Accepted: 2, Published: 1},
		"published": {Findings: 2, Accepted: 2, Published: 2},
		"triaged":   {Findings: 2},
	} {
		if got := info.Stage(); got != want {
			t.Errorf("%+v: stage %q, want %q", info, got, want)
		}
	}
}

func TestInitials(t *testing.T) {
	for _, tc := range []struct{ name, user, want string }{
		{"Alice Smith", "alice", "AS"},
		{"Marcus Vale (@marcus)", "", "MV"},
		{"Priya Ann Natarajan", "", "PN"},
		{"", "jane.doe", "JD"},
		{"alice", "", "A"},
	} {
		if got := initials(tc.name, tc.user); got != tc.want {
			t.Errorf("initials(%q, %q) = %q, want %q", tc.name, tc.user, got, tc.want)
		}
	}
}

func TestBandRendersReviewPipeline(t *testing.T) {
	env := newTestEnv(t, &fakeReviewer{result: defaultResult()})
	_, body := env.get("/i/default/mr?project=group%2Fapp&iid=5")
	if !strings.Contains(body, `class="step current" data-step="review"`) || !strings.Contains(body, "Run AI review") {
		t.Fatalf("unreviewed MR should lead with the review step:\n%s", body)
	}

	env.post("/i/default/mr/review", mrForm(url.Values{"agents": {"bug"}}))
	waitRun(t, env.srv)
	for _, page := range []string{"/i/default/mr?project=group%2Fapp&iid=5", "/i/default/mr/diff?project=group%2Fapp&iid=5", "/i/default/mr/history?project=group%2Fapp&iid=5"} {
		_, body := env.get(page)
		if !strings.Contains(body, `class="step current" data-step="triage"`) || !strings.Contains(body, "Continue triage") {
			t.Fatalf("%s: pending findings should make triage the next step:\n%s", page, body)
		}
	}
}
