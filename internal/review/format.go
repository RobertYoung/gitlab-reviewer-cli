package review

import "fmt"

// AttributionFooter is appended to published comments when
// publish.attribution is enabled.
const AttributionFooter = "\n\n---\n*🤖 suggested by [gitlab-reviewer](https://github.com/RobertYoung/gitlab-reviewer-cli), reviewed and posted by a human*"

// RenderBody formats a finding as the GitLab comment body. Suggestions
// become GitLab suggestion blocks only when anchored to a new-side line
// (GitLab applies suggestions to the commented line).
func (f Finding) RenderBody(attribution bool) string {
	body := fmt.Sprintf("**[%s · %s] %s**\n\n%s", f.Severity, f.Category, f.Title, f.Body)
	if f.Suggestion != "" && f.Line.NewLine != nil {
		body += "\n\n```suggestion:-0+0\n" + f.Suggestion + "\n```"
	}
	if attribution {
		body += AttributionFooter
	}
	return body
}

// RenderFallbackBody formats a finding for a general MR note when no inline
// position could be resolved; blobURL may be empty.
func (f Finding) RenderFallbackBody(attribution bool, blobURL string) string {
	loc := f.File
	if f.Line.NewLine != nil {
		loc = fmt.Sprintf("%s:%d", f.File, *f.Line.NewLine)
	} else if f.Line.OldLine != nil {
		loc = fmt.Sprintf("%s:%d (old)", f.File, *f.Line.OldLine)
	}
	header := fmt.Sprintf("**`%s`** *(could not anchor this comment inline)*", loc)
	if blobURL != "" {
		header = fmt.Sprintf("**[`%s`](%s)** *(could not anchor this comment inline)*", loc, blobURL)
	}
	return header + "\n\n" + f.RenderBody(attribution)
}
