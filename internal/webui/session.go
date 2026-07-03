package webui

import (
	"crypto/subtle"
	"net/http"
)

const sessionCookie = "gitlab_reviewer_session"

// auth gates every request behind the per-session token. The launch URL
// carries ?token=…; the first request exchanges it for a strict same-site
// cookie so links stay clean. Other local processes cannot drive the
// session without the token, and cross-origin POSTs are refused outright.
func (s *Server) auth(next http.Handler) http.Handler {
	// Sec-Fetch-Site based, with Origin fallback. The manual Origin==Host
	// check used before broke Firefox: under Referrer-Policy: no-referrer it
	// sends Origin: null even on same-origin form POSTs.
	csrf := http.NewCrossOriginProtection()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")

		// Cookie bootstrap: a GET with the right token gets the session
		// cookie and a redirect to the same URL without the token, so the
		// secret does not linger in the address bar or history links.
		if tok := r.URL.Query().Get("token"); tok != "" && r.Method == http.MethodGet {
			if !tokenEqual(tok, s.token) {
				s.renderForbidden(w)
				return
			}
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure would break the plain-http loopback server; HttpOnly and SameSite are set
				Name:     sessionCookie,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			clean := *r.URL
			q := clean.Query()
			q.Del("token")
			clean.RawQuery = q.Encode()
			http.Redirect(w, r, clean.String(), http.StatusSeeOther) //nolint:gosec // same request URL, only the token query param removed
			return
		}

		c, err := r.Cookie(sessionCookie)
		if err != nil || !tokenEqual(c.Value, s.token) {
			s.renderForbidden(w)
			return
		}

		// The session cookie is same-site strict, but belt-and-braces:
		// refuse state-changing requests coming from another site.
		if err := csrf.Check(r); err != nil {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// renderForbidden is self-contained (no stylesheet link) because assets sit
// behind the same gate.
func (s *Server) renderForbidden(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>gitlab-reviewer</title>` +
		`<body style="font-family:system-ui;background:#0d1117;color:#c9d1d9;display:grid;place-items:center;height:100vh;margin:0">` +
		`<div><h1>Session required</h1><p>Open the exact URL printed by <code>gitlab-reviewer gui</code> in the terminal.</p></div>`))
}
