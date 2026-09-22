package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ethanpil/portapixel/internal/version"
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
//
// The third answer, fresh, says that THIS call reached GitHub. It is a return value
// and not a field of the Lister: a field is shared, and the route read it in a
// second locked call, so a List of another request could clear the flag of the call
// that did the work. The route then wrote nothing to the table, and the cache was
// young, so nothing wrote for the next cacheLife either.
func (l *Lister) List(ctx context.Context, force bool) (list []GitHubRelease, errText string, fresh bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if force && !l.forcedAt.IsZero() && now.Sub(l.forcedAt) < forceGap {
		// A refresh button that somebody holds down.
		force = false
	}
	young := !l.fetched.IsZero() && now.Sub(l.fetched) < cacheLife
	recentFailure := !l.failedAt.IsZero() && now.Sub(l.failedAt) < failureMemo
	if !force && (young || recentFailure) {
		return l.cached, l.lastErr, false
	}
	if force {
		l.forcedAt = now
	}

	fetched, err := l.fetch(ctx)
	if err != nil {
		l.lastErr = err.Error()
		l.failedAt = time.Now()
		// Keep the cache. A list that we had is better than no list.
		return l.cached, l.lastErr, false
	}
	l.cached = fetched
	l.fetched = time.Now()
	l.failedAt = time.Time{}
	l.lastErr = ""
	return l.cached, "", true
}

// Release gives one release of the list by its version.
func (l *Lister) Release(ctx context.Context, version string) (GitHubRelease, error) {
	list, errText, _ := l.List(ctx, false)
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
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// GitHub asks every caller to name itself, and it answers some requests with a
	// 403 when the header is missing.
	req.Header.Set("User-Agent", "portapixel-server/"+version.Version)

	resp, err := l.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The body holds the reason, for example "API rate limit exceeded". Without it
		// the admin reads "403 Forbidden" and can act on nothing.
		return nil, fmt.Errorf("%s answered %s%s", url, resp.Status, apiMessage(resp.Body))
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

// apiMessage reads the "message" field of an error answer of the GitHub API. It
// gives an empty string when there is none, so the caller can add it to a sentence.
func apiMessage(body io.Reader) string {
	var answer struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(limitReader(body, maxErrorBytes)).Decode(&answer); err != nil {
		return ""
	}
	if answer.Message == "" {
		return ""
	}
	return ": " + answer.Message
}

// maxErrorBytes is how much of an error answer we read.
const maxErrorBytes = 8 << 10
