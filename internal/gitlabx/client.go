package gitlabx

import (
	"context"
	"fmt"
	"slices"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// Client implements Service against a real GitLab instance.
type Client struct {
	gl       *gitlab.Client
	projects []string
	groups   []string
}

// New builds a Client for the given instance. projects and groups are the
// full paths the MR list fans out over.
func New(baseURL, token string, projects, groups []string) (*Client, error) {
	gl, err := gitlab.NewClient(token, gitlab.WithBaseURL(baseURL))
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}
	return &Client{gl: gl, projects: projects, groups: groups}, nil
}

func (c *Client) ListOpenMergeRequests(ctx context.Context, filter MRFilter, page Page) ([]MRSummary, bool, error) {
	var (
		all     []MRSummary
		seen    = map[int64]bool{}
		hasMore bool
	)

	listOpts := gitlab.ListOptions{Page: int64(page.Number), PerPage: int64(page.PerPage)}

	projects, groups := c.projects, c.groups
	if len(filter.Projects) > 0 || len(filter.Groups) > 0 {
		projects, groups = filter.Projects, filter.Groups
	}

	for _, project := range projects {
		opts := &gitlab.ListProjectMergeRequestsOptions{ListOptions: listOpts}
		applyFilter(&opts.State, &opts.AuthorUsername, &opts.TargetBranch, &opts.Search, filter)
		mrs, resp, err := c.gl.MergeRequests.ListProjectMergeRequests(project, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, false, fmt.Errorf("listing MRs for project %s: %w", project, err)
		}
		hasMore = hasMore || resp.NextPage > 0
		collect(&all, seen, mrs, project)
	}

	for _, group := range groups {
		opts := &gitlab.ListGroupMergeRequestsOptions{ListOptions: listOpts}
		applyFilter(&opts.State, &opts.AuthorUsername, &opts.TargetBranch, &opts.Search, filter)
		mrs, resp, err := c.gl.MergeRequests.ListGroupMergeRequests(group, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, false, fmt.Errorf("listing MRs for group %s: %w", group, err)
		}
		hasMore = hasMore || resp.NextPage > 0
		collect(&all, seen, mrs, "")
	}

	slices.SortFunc(all, func(a, b MRSummary) int {
		return b.UpdatedAt.Compare(a.UpdatedAt)
	})
	return all, hasMore, nil
}

func applyFilter(state, author, target, search **string, f MRFilter) {
	if f.State == "" {
		*state = gitlab.Ptr("opened")
	} else if f.State != "all" {
		*state = gitlab.Ptr(f.State)
	}
	if f.AuthorUsername != "" {
		*author = gitlab.Ptr(f.AuthorUsername)
	}
	if f.TargetBranch != "" {
		*target = gitlab.Ptr(f.TargetBranch)
	}
	if f.Search != "" {
		*search = gitlab.Ptr(f.Search)
	}
}

func collect(all *[]MRSummary, seen map[int64]bool, mrs []*gitlab.BasicMergeRequest, projectPath string) {
	for _, mr := range mrs {
		if seen[mr.ID] {
			continue
		}
		seen[mr.ID] = true
		*all = append(*all, toSummary(mr, projectPath))
	}
}

func toSummary(mr *gitlab.BasicMergeRequest, projectPath string) MRSummary {
	s := MRSummary{
		ProjectID:    mr.ProjectID,
		ProjectPath:  projectPath,
		IID:          mr.IID,
		Title:        mr.Title,
		Description:  mr.Description,
		State:        mr.State,
		Draft:        mr.Draft,
		SourceBranch: mr.SourceBranch,
		TargetBranch: mr.TargetBranch,
		HeadSHA:      mr.SHA,
		WebURL:       mr.WebURL,
	}
	if s.ProjectPath == "" {
		// Group listings do not carry the project path directly; derive it
		// from the full reference ("group/app!42").
		if mr.References != nil {
			if path, _, found := strings.Cut(mr.References.Full, "!"); found {
				s.ProjectPath = path
			}
		}
	}
	if mr.Author != nil {
		s.Author = mr.Author.Username
	}
	if mr.CreatedAt != nil {
		s.CreatedAt = *mr.CreatedAt
	}
	if mr.UpdatedAt != nil {
		s.UpdatedAt = *mr.UpdatedAt
	}
	return s
}

func (c *Client) ListGroups(ctx context.Context, search string, page Page) ([]GroupInfo, bool, error) {
	opts := &gitlab.ListGroupsOptions{
		ListOptions: gitlab.ListOptions{Page: int64(page.Number), PerPage: int64(page.PerPage)},
		OrderBy:     gitlab.Ptr("path"),
		Sort:        gitlab.Ptr("asc"),
	}
	if search != "" {
		opts.Search = gitlab.Ptr(search)
	}
	groups, resp, err := c.gl.Groups.ListGroups(opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, false, fmt.Errorf("listing groups: %w", err)
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, GroupInfo{
			ID:          g.ID,
			FullPath:    g.FullPath,
			Name:        g.Name,
			Description: g.Description,
		})
	}
	return out, resp.NextPage > 0, nil
}

func (c *Client) ListGroupProjects(ctx context.Context, group string, search string, page Page) ([]ProjectInfo, bool, error) {
	opts := &gitlab.ListGroupProjectsOptions{
		ListOptions:              gitlab.ListOptions{Page: int64(page.Number), PerPage: int64(page.PerPage)},
		IncludeSubGroups:         gitlab.Ptr(true),
		WithMergeRequestsEnabled: gitlab.Ptr(true),
		Archived:                 gitlab.Ptr(false),
		OrderBy:                  gitlab.Ptr("last_activity_at"),
	}
	if search != "" {
		opts.Search = gitlab.Ptr(search)
	}
	projects, resp, err := c.gl.Groups.ListGroupProjects(group, opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, false, fmt.Errorf("listing projects of group %s: %w", group, err)
	}
	return toProjectInfos(projects), resp.NextPage > 0, nil
}

func (c *Client) ListMemberProjects(ctx context.Context, search string, page Page) ([]ProjectInfo, bool, error) {
	opts := &gitlab.ListProjectsOptions{
		ListOptions:              gitlab.ListOptions{Page: int64(page.Number), PerPage: int64(page.PerPage)},
		Membership:               gitlab.Ptr(true),
		WithMergeRequestsEnabled: gitlab.Ptr(true),
		Archived:                 gitlab.Ptr(false),
		OrderBy:                  gitlab.Ptr("last_activity_at"),
	}
	if search != "" {
		opts.Search = gitlab.Ptr(search)
	}
	projects, resp, err := c.gl.Projects.ListProjects(opts, gitlab.WithContext(ctx))
	if err != nil {
		return nil, false, fmt.Errorf("listing your projects: %w", err)
	}
	return toProjectInfos(projects), resp.NextPage > 0, nil
}

func toProjectInfos(projects []*gitlab.Project) []ProjectInfo {
	out := make([]ProjectInfo, 0, len(projects))
	for _, p := range projects {
		info := ProjectInfo{
			ID:                p.ID,
			PathWithNamespace: p.PathWithNamespace,
			Description:       p.Description,
		}
		if p.LastActivityAt != nil {
			info.LastActivity = *p.LastActivityAt
		}
		out = append(out, info)
	}
	return out
}

func (c *Client) GetMergeRequest(ctx context.Context, project any, iid int64) (*MRDetail, error) {
	mr, _, err := c.gl.MergeRequests.GetMergeRequest(project, iid, nil, gitlab.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("fetching MR !%d: %w", iid, err)
	}
	detail := &MRDetail{
		MRSummary: MRSummary{
			ProjectID:    mr.ProjectID,
			IID:          mr.IID,
			Title:        mr.Title,
			Description:  mr.Description,
			State:        mr.State,
			Draft:        mr.Draft,
			SourceBranch: mr.SourceBranch,
			TargetBranch: mr.TargetBranch,
			HeadSHA:      mr.SHA,
			WebURL:       mr.WebURL,
		},
		DiffRefs: DiffRefs{
			BaseSHA:  mr.DiffRefs.BaseSha,
			HeadSHA:  mr.DiffRefs.HeadSha,
			StartSHA: mr.DiffRefs.StartSha,
		},
		HasConflicts: mr.HasConflicts,
	}
	if path, ok := project.(string); ok {
		detail.ProjectPath = path
	} else if mr.References != nil {
		if path, _, found := strings.Cut(mr.References.Full, "!"); found {
			detail.ProjectPath = path
		}
	}
	if mr.Author != nil {
		detail.Author = mr.Author.Username
	}
	if mr.CreatedAt != nil {
		detail.CreatedAt = *mr.CreatedAt
	}
	if mr.UpdatedAt != nil {
		detail.UpdatedAt = *mr.UpdatedAt
	}
	return detail, nil
}

// maxDiffPages bounds internal pagination; 20 pages × 100 files covers any
// MR a human would review.
const maxDiffPages = 20

func (c *Client) ListDiffs(ctx context.Context, project any, iid int64) ([]FileDiff, error) {
	var out []FileDiff
	opts := &gitlab.ListMergeRequestDiffsOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100},
	}
	for page := 1; page <= maxDiffPages; page++ {
		opts.Page = int64(page)
		diffs, resp, err := c.gl.MergeRequests.ListMergeRequestDiffs(project, iid, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing diffs for MR !%d: %w", iid, err)
		}
		for _, d := range diffs {
			out = append(out, FileDiff{
				OldPath:       d.OldPath,
				NewPath:       d.NewPath,
				Diff:          d.Diff,
				NewFile:       d.NewFile,
				RenamedFile:   d.RenamedFile,
				DeletedFile:   d.DeletedFile,
				GeneratedFile: d.GeneratedFile,
				TooLarge:      d.TooLarge,
			})
		}
		if resp.NextPage == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) ListDiscussions(ctx context.Context, project any, iid int64) ([]Discussion, error) {
	var out []Discussion
	opts := &gitlab.ListMergeRequestDiscussionsOptions{
		ListOptions: gitlab.ListOptions{PerPage: 100},
	}
	for page := 1; page <= maxDiffPages; page++ {
		opts.Page = int64(page)
		discussions, resp, err := c.gl.Discussions.ListMergeRequestDiscussions(project, iid, opts, gitlab.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("listing discussions for MR !%d: %w", iid, err)
		}
		for _, d := range discussions {
			out = append(out, toDiscussion(d))
		}
		if resp.NextPage == 0 {
			break
		}
	}
	return out, nil
}

func (c *Client) CreateInlineDiscussion(ctx context.Context, project any, iid int64, body string, pos *Position) error {
	if pos == nil {
		return fmt.Errorf("inline discussion on MR !%d requires a position", iid)
	}
	opts := &gitlab.CreateMergeRequestDiscussionOptions{
		Body: gitlab.Ptr(body),
		Position: &gitlab.PositionOptions{
			BaseSHA:      gitlab.Ptr(pos.BaseSHA),
			HeadSHA:      gitlab.Ptr(pos.HeadSHA),
			StartSHA:     gitlab.Ptr(pos.StartSHA),
			OldPath:      gitlab.Ptr(pos.OldPath),
			NewPath:      gitlab.Ptr(pos.NewPath),
			PositionType: gitlab.Ptr("text"),
		},
	}
	if pos.OldLine != nil {
		opts.Position.OldLine = gitlab.Ptr(int64(*pos.OldLine))
	}
	if pos.NewLine != nil {
		opts.Position.NewLine = gitlab.Ptr(int64(*pos.NewLine))
	}
	if _, _, err := c.gl.Discussions.CreateMergeRequestDiscussion(project, iid, opts, gitlab.WithContext(ctx)); err != nil {
		return fmt.Errorf("creating inline discussion on MR !%d: %w", iid, err)
	}
	return nil
}

func (c *Client) CreateNote(ctx context.Context, project any, iid int64, body string) error {
	opts := &gitlab.CreateMergeRequestNoteOptions{Body: gitlab.Ptr(body)}
	if _, _, err := c.gl.Notes.CreateMergeRequestNote(project, iid, opts, gitlab.WithContext(ctx)); err != nil {
		return fmt.Errorf("creating note on MR !%d: %w", iid, err)
	}
	return nil
}

func (c *Client) CreateDraftNote(ctx context.Context, project any, iid int64, body string, pos *Position) error {
	opts := &gitlab.CreateDraftNoteOptions{Note: gitlab.Ptr(body)}
	if pos != nil {
		opts.Position = &gitlab.PositionOptions{
			BaseSHA:      gitlab.Ptr(pos.BaseSHA),
			HeadSHA:      gitlab.Ptr(pos.HeadSHA),
			StartSHA:     gitlab.Ptr(pos.StartSHA),
			OldPath:      gitlab.Ptr(pos.OldPath),
			NewPath:      gitlab.Ptr(pos.NewPath),
			PositionType: gitlab.Ptr("text"),
		}
		if pos.OldLine != nil {
			opts.Position.OldLine = gitlab.Ptr(int64(*pos.OldLine))
		}
		if pos.NewLine != nil {
			opts.Position.NewLine = gitlab.Ptr(int64(*pos.NewLine))
		}
	}
	if _, _, err := c.gl.DraftNotes.CreateDraftNote(project, iid, opts, gitlab.WithContext(ctx)); err != nil {
		return fmt.Errorf("creating draft note on MR !%d: %w", iid, err)
	}
	return nil
}

func (c *Client) PublishAllDraftNotes(ctx context.Context, project any, iid int64) error {
	if _, err := c.gl.DraftNotes.PublishAllDraftNotes(project, iid, gitlab.WithContext(ctx)); err != nil {
		return fmt.Errorf("publishing draft review on MR !%d: %w", iid, err)
	}
	return nil
}

func toDiscussion(d *gitlab.Discussion) Discussion {
	disc := Discussion{ID: d.ID}
	for _, n := range d.Notes {
		note := Note{
			ID:       n.ID,
			Body:     n.Body,
			System:   n.System,
			Resolved: n.Resolved,
		}
		if n.Author.Username != "" {
			note.Author = n.Author.Username
		}
		if n.CreatedAt != nil {
			note.CreatedAt = *n.CreatedAt
		}
		if n.Position != nil {
			note.Position = &Position{
				BaseSHA:  n.Position.BaseSHA,
				HeadSHA:  n.Position.HeadSHA,
				StartSHA: n.Position.StartSHA,
				OldPath:  n.Position.OldPath,
				NewPath:  n.Position.NewPath,
			}
			if n.Position.OldLine != 0 {
				old := int(n.Position.OldLine)
				note.Position.OldLine = &old
			}
			if n.Position.NewLine != 0 {
				newLine := int(n.Position.NewLine)
				note.Position.NewLine = &newLine
			}
		}
		disc.Notes = append(disc.Notes, note)
	}
	return disc
}
