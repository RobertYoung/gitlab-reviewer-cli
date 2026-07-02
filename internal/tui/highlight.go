package tui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromastyles "github.com/alecthomas/chroma/v2/styles"
)

// highlighter colours source lines for one file. Lines are tokenised
// individually — multi-line constructs (block comments, raw strings) may
// lose state across lines, which is the usual trade-off in diff viewers.
type highlighter struct {
	lexer     chroma.Lexer
	formatter chroma.Formatter
	style     *chroma.Style
}

func newHighlighter(filename string) *highlighter {
	lexer := lexers.Match(filename)
	if lexer == nil {
		return nil
	}
	return &highlighter{
		lexer:     chroma.Coalesce(lexer),
		formatter: formatters.Get("terminal256"),
		style:     chromastyles.Get("monokai"),
	}
}

// line returns the syntax-highlighted rendering of one source line, or the
// input unchanged if highlighting fails.
func (h *highlighter) line(code string) string {
	if h == nil || code == "" {
		return code
	}
	iter, err := h.lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}
	var b strings.Builder
	if err := h.formatter.Format(&b, h.style, iter); err != nil {
		return code
	}
	// Tokenising can add a trailing newline; the caller owns line breaks.
	return strings.TrimSuffix(b.String(), "\n")
}
