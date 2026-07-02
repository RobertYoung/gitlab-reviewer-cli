package tui

import (
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"
)

// openBrowser opens url in the platform's default browser. The URL is a
// single argv element (no shell), so it cannot inject commands.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start() //nolint:gosec // url from the GitLab API, passed as one arg
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start() //nolint:gosec // url from the GitLab API, passed as one arg
	default:
		return exec.Command("xdg-open", url).Start() //nolint:gosec // url from the GitLab API, passed as one arg
	}
}

// openURLCmd opens url via the injected opener (or the default browser)
// off the UI goroutine. A failed launch is not fatal to the session, so
// the error is dropped rather than replacing the active screen.
func openURLCmd(d Deps, url string) tea.Cmd {
	if url == "" {
		return nil
	}
	return func() tea.Msg {
		_ = d.openURL(url)
		return nil
	}
}
