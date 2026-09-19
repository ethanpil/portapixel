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

// GitHubRelease is the part of a GitHub release that we use.
type GitHubRelease struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
	// Assets maps a file name to the URL that serves it.
	Assets map[string]string `json:"-"`
}

// apiRelease is the shape that the GitHub API sends.
type apiRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
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

	mu       sync.Mutex
	cached   []GitHubRelease
	fetched  time.Time
	lastErr  string
	fetching bool
}

// NewLister makes a Lister for one repository.
func NewLister(repo string) *Lister {
	return &Lister{
		Repo:    repo,
		Client:  &http.Client{Timeout: listTimeout},
		BaseURL: "https://api.github.com",
	}
}

// List gives the release list. It answers from the cache while the cache is
// young. A failure gives the last good list and the error text, because a server
// with no way to reach GitHub must still show the releases that it knows and the
// version that it mirrors (plan section 12).
func (l *Lister) List(ctx context.Context, force bool) ([]GitHubRelease, string) {
	l.mu.Lock()
	fresh := time.Since(l.fetched) < cacheLife && l.fetched != (time.Time{})
	if !force && fresh {
		out, errText := l.cached, l.lastErr
		l.mu.Unlock()
		return out, errText
	}
	l.mu.Unlock()

	list, err := l.fetch(ctx)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.lastErr = err.Error()
		// Keep the cache. A list that we had is better than no list.
		return l.cached, l.lastErr
	}
	l.cached = list
	l.fetched = time.Now()
	l.lastErr = ""
	return l.cached, ""
}

// Release gives one release of the list by its version.
func (l *Lister) Release(ctx context.Context, version string) (GitHubRelease, error) {
	list, errText := l.List(ctx, false)
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
	dec := json.NewDecoder(newLimitReader(resp.Body, maxListBytes))
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
			Prerelease:  r.Prerelease,
			Assets:      map[string]string{},
		}
		for _, a := range r.Assets {
			rel.Assets[a.Name] = a.URL
		}
		out = append(out, rel)
	}
	return out, nil
}
