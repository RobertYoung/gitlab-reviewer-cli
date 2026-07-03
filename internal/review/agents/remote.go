package agents

import "sync"

// RemoteCache memoises the agent definition files fetched from project
// repositories, keyed by (project, sha), so reopening a picker or reloading
// the review form does not refetch them. The sha key means a new MR head
// (which may add or edit agents) fetches fresh.
type RemoteCache struct {
	mu      sync.Mutex
	entries map[string][]File
}

// NewRemoteCache builds an empty cache.
func NewRemoteCache() *RemoteCache {
	return &RemoteCache{entries: map[string][]File{}}
}

// Extend returns base extended with the project's fetched agent files,
// calling fetch on first use per (project, sha). On fetch failure base is
// returned unchanged alongside the error; failures are not cached, so the
// next open retries. A nil receiver still fetches, just without caching.
func (rc *RemoteCache) Extend(base *Catalog, projectPath, sha string, fetch func() ([]File, error)) (*Catalog, error) {
	if rc == nil {
		files, err := fetch()
		if err != nil {
			return base, err
		}
		return base.WithProjectFiles(files), nil
	}
	// The lock is held across fetch so concurrent opens of the same MR do
	// not fetch twice; contention is negligible for a single-user UI.
	rc.mu.Lock()
	defer rc.mu.Unlock()
	key := projectPath + "@" + sha
	files, ok := rc.entries[key]
	if !ok {
		var err error
		files, err = fetch()
		if err != nil {
			return base, err
		}
		rc.entries[key] = files
	}
	return base.WithProjectFiles(files), nil
}
