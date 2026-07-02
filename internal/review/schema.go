package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OutputSchema is passed to the backend (claude --json-schema) so findings
// arrive as validated structured output.
const OutputSchema = `{
  "type": "object",
  "required": ["summary", "findings"],
  "properties": {
    "summary": {
      "type": "string",
      "description": "Two or three sentences summarising the change and overall review verdict"
    },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["file", "severity", "category", "title", "body"],
        "properties": {
          "file": {"type": "string", "description": "Repository-relative path (the NEW path for renamed files)"},
          "old_file": {"type": ["string", "null"], "description": "Old path if the file was renamed"},
          "new_line": {"type": ["integer", "null"], "description": "Line number in the new file; null for findings on removed lines"},
          "old_line": {"type": ["integer", "null"], "description": "Line number in the old file; only for removed or unchanged lines"},
          "severity": {"enum": ["critical", "major", "minor", "info"]},
          "category": {"enum": ["bug", "security", "performance", "docs", "style", "design"]},
          "title": {"type": "string", "description": "One-line summary of the issue"},
          "body": {"type": "string", "description": "The full review comment, GitLab-flavoured markdown"},
          "suggestion": {"type": ["string", "null"], "description": "Optional replacement text for exactly the flagged line"}
        }
      }
    }
  }
}`

// rawResult mirrors OutputSchema for decoding.
type rawResult struct {
	Summary  string       `json:"summary"`
	Findings []rawFinding `json:"findings"`
}

type rawFinding struct {
	File       string  `json:"file"`
	OldFile    *string `json:"old_file"`
	NewLine    *int    `json:"new_line"`
	OldLine    *int    `json:"old_line"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Title      string  `json:"title"`
	Body       string  `json:"body"`
	Suggestion *string `json:"suggestion"`
}

// ParseResult decodes and validates a backend's structured output. Findings
// that fail validation are dropped into Warnings rather than failing the
// review; a completely undecodable payload is an error.
func ParseResult(data []byte) (*Result, error) {
	var raw rawResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("review output is not valid JSON: %w", err)
	}

	res := &Result{Summary: strings.TrimSpace(raw.Summary), Raw: data}
	for i, rf := range raw.Findings {
		f, err := rf.validate(i)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		res.Findings = append(res.Findings, f)
	}
	return res, nil
}

func (rf rawFinding) validate(idx int) (Finding, error) {
	fail := func(msg string) (Finding, error) {
		title := rf.Title
		if title == "" {
			title = fmt.Sprintf("finding %d", idx+1)
		}
		return Finding{}, fmt.Errorf("dropped %q: %s", title, msg)
	}
	if strings.TrimSpace(rf.File) == "" {
		return fail("missing file path")
	}
	if strings.TrimSpace(rf.Title) == "" || strings.TrimSpace(rf.Body) == "" {
		return fail("missing title or body")
	}
	sev := Severity(rf.Severity)
	if !sev.Valid() {
		return fail("unknown severity " + rf.Severity)
	}
	cat := Category(rf.Category)
	if !cat.Valid() {
		return fail("unknown category " + rf.Category)
	}
	if rf.NewLine == nil && rf.OldLine == nil {
		return fail("no line number")
	}
	if (rf.NewLine != nil && *rf.NewLine < 1) || (rf.OldLine != nil && *rf.OldLine < 1) {
		return fail("line numbers must be positive")
	}

	f := Finding{
		ID:       fmt.Sprintf("f%03d", idx+1),
		File:     strings.TrimPrefix(strings.TrimSpace(rf.File), "./"),
		Line:     LineRef{OldLine: rf.OldLine, NewLine: rf.NewLine},
		Severity: sev,
		Category: cat,
		Title:    strings.TrimSpace(rf.Title),
		Body:     strings.TrimSpace(rf.Body),
	}
	if rf.OldFile != nil {
		f.OldFile = strings.TrimSpace(*rf.OldFile)
	}
	if rf.Suggestion != nil {
		f.Suggestion = strings.TrimRight(*rf.Suggestion, "\n")
	}
	return f, nil
}
