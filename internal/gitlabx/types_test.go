package gitlabx

import "testing"

func TestMRSummaryWebURLs(t *testing.T) {
	m := MRSummary{
		ProjectPath:  "group/app",
		IID:          5,
		Author:       "alice",
		SourceBranch: "feat/nested branch",
		TargetBranch: "main",
		WebURL:       "https://gitlab.example.com/group/app/-/merge_requests/5",
	}

	if got, want := m.ProjectWebURL(), "https://gitlab.example.com/group/app"; got != want {
		t.Errorf("ProjectWebURL = %q, want %q", got, want)
	}
	if got, want := m.AuthorWebURL(), "https://gitlab.example.com/alice"; got != want {
		t.Errorf("AuthorWebURL = %q, want %q", got, want)
	}
	// slashes in branch names stay literal; other specials are escaped
	if got, want := m.BranchWebURL(m.SourceBranch), "https://gitlab.example.com/group/app/-/tree/feat/nested%20branch"; got != want {
		t.Errorf("BranchWebURL(source) = %q, want %q", got, want)
	}
	if got, want := m.BranchWebURL(m.TargetBranch), "https://gitlab.example.com/group/app/-/tree/main"; got != want {
		t.Errorf("BranchWebURL(target) = %q, want %q", got, want)
	}

	// Without a WebURL nothing is derivable — all links degrade to empty.
	var zero MRSummary
	zero.Author = "alice"
	if zero.ProjectWebURL() != "" || zero.AuthorWebURL() != "" || zero.BranchWebURL("main") != "" {
		t.Errorf("zero WebURL should yield empty links, got %q %q %q",
			zero.ProjectWebURL(), zero.AuthorWebURL(), zero.BranchWebURL("main"))
	}
}

func TestDiscussionThreadStates(t *testing.T) {
	cases := []struct {
		name       string
		d          Discussion
		resolvable bool
		unresolved bool
	}{
		{"open thread", Discussion{Notes: []Note{{Resolvable: true}}}, true, true},
		{"resolved thread", Discussion{Notes: []Note{{Resolvable: true, Resolved: true}}}, true, false},
		// A thread resolves only when every resolvable note is resolved.
		{"partially resolved", Discussion{Notes: []Note{
			{Resolvable: true, Resolved: true}, {Resolvable: true},
		}}, true, true},
		{"plain comment", Discussion{Notes: []Note{{}}}, false, false},
		{"system note", Discussion{Notes: []Note{{System: true}}}, false, false},
		{"empty", Discussion{}, false, false},
	}
	for _, c := range cases {
		if got := c.d.Resolvable(); got != c.resolvable {
			t.Errorf("%s: Resolvable() = %v, want %v", c.name, got, c.resolvable)
		}
		if got := c.d.Unresolved(); got != c.unresolved {
			t.Errorf("%s: Unresolved() = %v, want %v", c.name, got, c.unresolved)
		}
	}
}
