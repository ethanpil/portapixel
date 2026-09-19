package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// cacheLife is how long a release list stays good. The GitHub API limits an
// unauthenticated caller to 60 requests an hour from one address, so the server
// must not ask on every page view.
const cacheLife = 10 * time.Minute

// listTimeout is how long the server waits for the GitHub API.
const listTimeout = 20 * time.Second

// failureMemo is how long a failed fetch is remembered. An offline server would
// otherwise wait listTimeout on every page view of the Versions page, so the page
// would take 20 s each time instead of answering at once with the error text.
const failureMemo = 2 * time.Minute

// forceGap is the shortest time between two forced fetches. The refresh button
// skips the cache, so without a gap a person who holds it down would use up the
// hourly quota of the GitHub API.
const forceGap = time.Minute

// GitHubRelease is the part of a GitHub release that we use.
type GitHubRelease struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`
	// Assets maps a file name to the URL that serves it.
	Assets map[string]string `json:"-"`
}

// apiRelease is the shape that the GitHub API sends.
type apiRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// maxListBytes is the largest release list that we read. A list of thirty
// releases with long notes is far under it, and a body with no end must not fill
// the memory of the server.
const maxListBytes = 4 << 20

// Lister reads the release list of one repository and keeps it for cacheLife.
type Lister struct {
	// Repo is "owner/name".
	Repo string
	// Client makes the requests. A test gives one that answers from a stub.
	Client *http.Client
	// BaseURL is the root of the API. A test replaces it.
	BaseURL string

	mu      sync.Mutex
	cached  []GitHubRelease
	fetched time.Time
	lastErr string
	// failedAt is when the last fetch failed. See failureMemo.
	failedAt time.Time
	// forcedAt is when the last forced fetch ran. See forceGap.
	forcedAt time.Time
	// fresh is true after a fetch that really reached GitHub, until the next
	// List. The releases route writes the table only then, so a page view that
	// answers from the cache costs no write.
	fresh bool
}

// NewLister makes a Lister for one repository.
func NewLister(repo string) *Lister {
	return &Lister{
		Repo:    repo,
		Client:  &http.Client{Timeout: listTimeout},
		BaseURL: "https://api.github.com",
	}
}

// List gives the release list. It answers from the cache while the cache is young.
// A failure gives the last good list and the error text, because a server with no
// way to reach GitHub must still show the releases that it knows and the version
// that it mirrors (plan section 12).
//
// The mutex is held across the fetch. That makes List single-flight: two admin
// pages that load together make one request to GitHub and not two. The API limits
// an unauthenticated caller to 60 requests an hour from one address, so a second
// request buys nothing and costs a tenth of the hour.
//
// ctx says when to give up. The caller passes a context of the life of the server
// and not the context of its request: a page that the admin leaves would otherwise
// write "context canceled" into the error text that every later page reads.
func (l *Lister) List(ctx context.Context, force bool) ([]GitHubRelease, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fresh = false

	now := time.Now()
	if force && !l.forcedAt.IsZero() && now.Sub(l.forcedAt) < forceGap {
		// A refresh button that somebody holds down.
		force = false
	}
	young := !l.fetched.IsZero() && now.Sub(l.fetched) < cacheLife
	recentFailure := !l.failedAt.IsZero() && now.Sub(l.failedAt) < failureMemo
	if !force && (young || recentFailure) {
		return l.cached, l.lastErr
	}
	if force {
		l.forcedAt = now
	}

	list, err := l.fetch(ctx)
	if err != nil {
		l.lastErr = err.Error()
		l.failedAt = time.Now()
		// Keep the cache. A list that we had is better than no list.
		return l.cached, l.lastErr
	}
	l.cached = list
	l.fetched = time.Now()
	l.failedAt = time.Time{}
	l.lastErr = ""
	l.fresh = true
	return l.cached, ""
}

// Fresh reports if the last List really reached GitHub. The releases route writes
// the table only then: a write for each of thirty releases on every page view would
// fight every heartbeat of the fleet for the one write connection.
func (l *Lister) Fresh() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fresh
}

// Release gives one release of the list by its version.
func (l *Lister) Release(ctx context.Context, version string) (GitHubRelease, error) {
	list, errText := l.list(ctx)
	for _, r := range list {
		if r.Version == version {
			return r, nil
		}
	}
	if errText != "" {
		return GitHubRelease{}, fmt.Errorf("the release list of %s is not available: %s", l.Repo, errText)
	}
	return GitHubRelease{}, fmt.Errorf("the release list of %s holds no version %s", l.Repo, version)
}

// fetch asks the GitHub API.
func (l *Lister) fetch(ctx context.Context) ([]GitHubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=30", l.BaseURL, l.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}

	var raw []apiRelease
	dec := json.NewDecoder(limitReader(resp.Body, maxListBytes))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("read the release list of %s: %w", l.Repo, err)
	}

	out := make([]GitHubRelease, 0, len(raw))
	for _, r := range raw {
		if r.Draft || r.TagName == "" {
			continue
		}
		rel := GitHubRelease{
			Version:     r.TagName,
			Notes:       r.Body,
			PublishedAt: r.PublishedAt,
			Assets:      map[string]string{},
		}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		out = append(out, rel)
	}
	return out, nil
}

// list gives the release list with no force. It is here so that Release does not
// clear the Fresh flag of a List that the route made just before it.
func (l *Lister) list(ctx context.Context) ([]GitHubRelease, string) {
	l.mu.Lock()
	young := !l.fetched.IsZero() && time.Since(l.fetched) < cacheLife
	if young {
		out, errText := l.cached, l.lastErr
		l.mu.Unlock()
		return out, errText
	}
	l.mu.Unlock()
	return l.List(ctx, false)
}
