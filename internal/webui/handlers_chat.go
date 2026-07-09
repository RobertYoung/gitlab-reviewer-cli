package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/RobertYoung/gitlab-reviewer-cli/internal/review"
)

// handleChatStart creates a conversation about the MR — or about one diff
// line when the form carries a file/old/new anchor — and redirects to its
// page. Text in the launching form's body field becomes the first message.
func (s *Server) handleChatStart(w http.ResponseWriter, r *http.Request, d *Deps) {
	inst := r.PathValue("inst")
	if d.Chatter == nil {
		s.renderError(w, http.StatusNotImplemented, errors.New("chat is not available with this reviewer backend"))
		return
	}
	detail, err := fetchDetail(r, d)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}
	diffs, err := d.Svc.ListDiffs(r.Context(), detail.Project(), detail.IID)
	if err != nil {
		s.renderError(w, http.StatusBadGateway, err)
		return
	}

	var focus *review.ChatFocus
	if file := r.FormValue("file"); file != "" {
		focus = &review.ChatFocus{File: file}
		if n, err := strconv.Atoi(r.FormValue("new")); err == nil && n > 0 {
			focus.Line.NewLine = &n
		}
		if o, err := strconv.Atoi(r.FormValue("old")); err == nil && o > 0 {
			focus.Line.OldLine = &o
		}
	}

	cs := s.chats.create(d, inst, *detail, diffs, focus)
	if msg := strings.TrimSpace(r.FormValue("body")); msg != "" {
		cs.startTurn(msg)
	}
	http.Redirect(w, r, instPath(inst, "/chat/"+cs.ID), http.StatusSeeOther) //nolint:gosec // server-built path: escaped instance + generated chat ID
}

type chatContent struct {
	Nav        mrNav
	State      chatSnapshot
	FocusLabel string // empty when the chat is about the whole MR
	Ref        string
	Title      string
	WebURL     string
	SendURL    string
	EventsURL  string
	CancelURL  string
	EndURL     string
}

// chatFromRequest resolves the {chat} path segment, nil when it is unknown
// or belongs to another instance.
func (s *Server) chatFromRequest(r *http.Request) *chatSession {
	cs := s.chats.get(r.PathValue("chat"))
	if cs == nil || cs.Instance != r.PathValue("inst") {
		return nil
	}
	return cs
}

// handleChatPage shows one conversation: the transcript, the reply in
// progress (streamed via SSE), and the composer.
func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	cs := s.chatFromRequest(r)
	if cs == nil {
		s.renderError(w, http.StatusNotFound, errors.New("unknown chat"))
		return
	}
	base := instPath(inst, "/chat/"+cs.ID)
	content := chatContent{
		Nav:        newMRNav(inst, cs.Project, cs.IID),
		State:      cs.snapshot(),
		FocusLabel: cs.FocusLabel(),
		Ref:        cs.Ref,
		Title:      cs.Title,
		WebURL:     cs.WebURL,
		SendURL:    base + "/send",
		EventsURL:  base + "/events",
		CancelURL:  base + "/cancel",
		EndURL:     base + "/end",
	}
	title := "chat · " + cs.Ref
	if content.FocusLabel != "" {
		title = "chat · " + content.FocusLabel
	}
	s.render(w, http.StatusOK, "chat", pageData{
		Title: title, Instance: inst,
		Crumbs: mrCrumbs(content.Nav, cs.Ref, "chat"), Content: content,
	})
}

// handleChatSend runs one turn with the posted message.
func (s *Server) handleChatSend(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	cs := s.chatFromRequest(r)
	if cs == nil {
		s.renderError(w, http.StatusNotFound, errors.New("unknown chat"))
		return
	}
	if msg := strings.TrimSpace(r.FormValue("message")); msg != "" {
		cs.startTurn(msg) // a false return means busy/closed; the page shows why
	}
	http.Redirect(w, r, instPath(inst, "/chat/"+cs.ID), http.StatusSeeOther) //nolint:gosec // server-built path: escaped instance + generated chat ID
}

// handleChatEvents streams the progress of the reply being written as
// server-sent events, ending with "done" when the page should re-render.
func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request, _ *Deps) {
	cs := s.chatFromRequest(r)
	if cs == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	writeLine := func(line string) {
		data, _ := json.Marshal(line)
		_, _ = fmt.Fprintf(w, "event: line\ndata: %s\n\n", data)
	}
	writeDone := func() {
		_, _ = fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	}

	replay, done, ch := cs.subscribe()
	for _, l := range replay {
		writeLine(l)
	}
	if done {
		writeDone()
		flusher.Flush()
		return
	}
	defer cs.unsubscribe(ch)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if ev.Done {
				writeDone()
				flusher.Flush()
				return
			}
			writeLine(ev.Line)
			flusher.Flush()
		}
	}
}

// handleChatCancel stops the reply being written; the conversation stays
// open.
func (s *Server) handleChatCancel(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	cs := s.chatFromRequest(r)
	if cs == nil {
		s.renderError(w, http.StatusNotFound, errors.New("unknown chat"))
		return
	}
	cs.mu.Lock()
	if cs.cancelTurn != nil {
		cs.appendStatusLocked("cancelling…")
		cs.cancelTurn()
	}
	cs.mu.Unlock()
	http.Redirect(w, r, instPath(inst, "/chat/"+cs.ID), http.StatusSeeOther) //nolint:gosec // server-built path: escaped instance + generated chat ID
}

// handleChatEnd closes the conversation and releases its checkout.
func (s *Server) handleChatEnd(w http.ResponseWriter, r *http.Request, _ *Deps) {
	inst := r.PathValue("inst")
	cs := s.chatFromRequest(r)
	if cs == nil {
		s.renderError(w, http.StatusNotFound, errors.New("unknown chat"))
		return
	}
	cs.close(r.Context())
	localRedirect(w, r, r.FormValue("back"), mrURL(inst, "/mr", cs.Project, cs.IID, nil))
}
