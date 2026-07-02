package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/gitlabx"
)

// treeNode is one row in the file explorer: a directory with children or a
// changed file pointing back into the MR's diff slice.
type treeNode struct {
	name      string
	parent    *treeNode
	children  []*treeNode
	collapsed bool
	diffIdx   int // index into the diffs slice; -1 for directories
	depth     int
}

func (n *treeNode) isDir() bool { return n.diffIdx < 0 }

// fileTree is the explorer sidebar: an MR's changed files grouped into a
// collapsible directory tree with a cursor over the visible rows.
type fileTree struct {
	root   *treeNode
	rows   []*treeNode // visible rows in render order
	cursor int
	offset int         // first row shown when the tree overflows its height
	counts map[int]int // diffIdx -> anchored discussion threads on that file
}

func newFileTree(diffs []gitlabx.FileDiff) *fileTree {
	root := &treeNode{diffIdx: -1}
	for i, fd := range diffs {
		path := fd.NewPath
		if path == "" {
			path = fd.OldPath
		}
		parts := strings.Split(path, "/")
		node := root
		for _, dir := range parts[:len(parts)-1] {
			node = node.childDir(dir)
		}
		node.children = append(node.children, &treeNode{name: parts[len(parts)-1], parent: node, diffIdx: i})
	}
	sortTree(root)
	t := &fileTree{root: root}
	t.flatten()
	return t
}

// childDir returns the existing directory child called name, creating it on
// first use so sibling files share one node.
func (n *treeNode) childDir(name string) *treeNode {
	for _, c := range n.children {
		if c.isDir() && c.name == name {
			return c
		}
	}
	c := &treeNode{name: name, parent: n, diffIdx: -1}
	n.children = append(n.children, c)
	return c
}

func sortTree(n *treeNode) {
	sort.SliceStable(n.children, func(i, j int) bool {
		a, b := n.children[i], n.children[j]
		if a.isDir() != b.isDir() {
			return a.isDir()
		}
		return a.name < b.name
	})
	for _, c := range n.children {
		if c.isDir() {
			sortTree(c)
		}
	}
}

// flatten recomputes the visible rows after a fold/unfold.
func (t *fileTree) flatten() {
	t.rows = t.rows[:0]
	var walk func(n *treeNode, depth int)
	walk = func(n *treeNode, depth int) {
		for _, c := range n.children {
			c.depth = depth
			t.rows = append(t.rows, c)
			if c.isDir() && !c.collapsed {
				walk(c, depth+1)
			}
		}
	}
	walk(t.root, 0)
	t.cursor = min(t.cursor, max(len(t.rows)-1, 0))
}

func (t *fileTree) move(delta int) {
	t.cursor = min(max(t.cursor+delta, 0), max(len(t.rows)-1, 0))
}

func (t *fileTree) first() { t.cursor = 0 }
func (t *fileTree) last()  { t.cursor = max(len(t.rows)-1, 0) }

func (t *fileTree) selected() *treeNode {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return nil
	}
	return t.rows[t.cursor]
}

func (t *fileTree) cursorTo(target *treeNode) {
	for i, n := range t.rows {
		if n == target {
			t.cursor = i
			return
		}
	}
}

// toggle folds or unfolds the directory under the cursor.
func (t *fileTree) toggle() {
	n := t.selected()
	if n == nil || !n.isDir() {
		return
	}
	n.collapsed = !n.collapsed
	t.flatten()
	t.cursorTo(n)
}

// collapseOrUp folds the directory under the cursor, or moves to the parent
// when the cursor is on a file or an already-folded directory.
func (t *fileTree) collapseOrUp() {
	n := t.selected()
	if n == nil {
		return
	}
	if n.isDir() && !n.collapsed {
		n.collapsed = true
		t.flatten()
		t.cursorTo(n)
		return
	}
	if p := n.parent; p != nil && p != t.root {
		t.cursorTo(p)
	}
}

// reveal expands the ancestors of the file with the given diff index and
// moves the cursor to it, keeping the explorer in sync with n/p navigation.
func (t *fileTree) reveal(diffIdx int) {
	node := findDiff(t.root, diffIdx)
	if node == nil {
		return
	}
	for p := node.parent; p != nil; p = p.parent {
		p.collapsed = false
	}
	t.flatten()
	t.cursorTo(node)
}

func findDiff(n *treeNode, diffIdx int) *treeNode {
	for _, c := range n.children {
		if c.diffIdx == diffIdx {
			return c
		}
		if c.isDir() {
			if f := findDiff(c, diffIdx); f != nil {
				return f
			}
		}
	}
	return nil
}

// setDiscussions recounts anchored threads per file, mirroring the path
// match discussionBlocks uses to place threads in the diff.
func (t *fileTree) setDiscussions(diffs []gitlabx.FileDiff, discussions []gitlabx.Discussion) {
	t.counts = map[int]int{}
	for _, d := range discussions {
		anchor := d.Anchor()
		if anchor == nil {
			continue
		}
		for i, fd := range diffs {
			if anchor.NewPath == fd.NewPath || anchor.OldPath == fd.OldPath {
				t.counts[i]++
				break
			}
		}
	}
}

// statusGlyph is the one-cell change indicator shown before each file.
func statusGlyph(fd gitlabx.FileDiff) string {
	switch {
	case fd.NewFile:
		return addedStyle.Render("A")
	case fd.DeletedFile:
		return removedStyle.Render("D")
	case fd.RenamedFile:
		return hunkStyle.Render("R")
	default:
		return draftStyle.Render("M")
	}
}

// view renders the tree as a block of exactly width-cell lines, scrolled so
// the cursor stays visible within height rows. currentIdx marks the file
// whose diff is on screen.
func (t *fileTree) view(diffs []gitlabx.FileDiff, width, height int, focused bool, currentIdx int) string {
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+height {
		t.offset = t.cursor - height + 1
	}
	t.offset = min(t.offset, max(len(t.rows)-height, 0))

	var lines []string
	for i := t.offset; i < len(t.rows) && i < t.offset+height; i++ {
		n := t.rows[i]
		prefix := "  "
		if focused && i == t.cursor {
			prefix = "> "
		}
		indent := strings.Repeat("  ", n.depth)
		var line string
		if n.isDir() {
			marker := "▾ "
			if n.collapsed {
				marker = "▸ "
			}
			name := truncate(n.name+"/", max(width-len(prefix)-len(indent)-2, 1))
			line = prefix + indent + subtleStyle.Render(marker) + name
		} else {
			count := ""
			if c := t.counts[n.diffIdx]; c > 0 {
				count = fmt.Sprintf(" 💬%d", c)
			}
			name := truncate(n.name, max(width-len(prefix)-len(indent)-2-lipgloss.Width(count), 1))
			if n.diffIdx == currentIdx {
				name = fileStyle.Render(name)
			}
			line = prefix + indent + statusGlyph(diffs[n.diffIdx]) + " " + name + subtleStyle.Render(count)
		}
		if pad := width - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
