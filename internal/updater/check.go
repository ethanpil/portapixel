package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// defaultGitHubAPI is the root of the GitHub API. A test replaces it through
// Source.
const defaultGitHubAPI = "https://api.github.com"

// maxAPIBytes is the largest release document that the updater reads. A release
// note of a few pages is far under it, and a body with no end must not fill the
// memory of a device with 512 MB.
const maxAPIBytes = 1 << 20

// Source says where a release comes from.
//
// A standalone device asks GitHub Releases. A paired device gets a base URL from
// the fleet server, which mirrors the approved release. The two are the same three
// files, so only the way to find their addresses is different (D28).
type Source struct {
	// Repo is "owner/name". It asks GitHub for the newest release that is not a
	// prerelease.
	Repo string
	// APIRoot replaces the GitHub API address. A test sets it.
	APIRoot string
	// BaseURL is the directory of a release on a fleet server. The three files are
	// under it with their own names. A path with no host, for example
	// "/api/v1/releases/1.5.0", is resolved against ServerURL: the manifest of the
	// server gives a path and not a whole address.
	BaseURL string
	// ServerURL is the address of the fleet server, for example
	// "https://signage.example.com". A relative BaseURL needs it.
	ServerURL string
	// Bearer is the device token. The release mirror of the server needs it on
	// every one of the three files. It goes to the fleet host and to no other:
	// see fleetRelease and mustSameHost.
	Bearer string
	// Version is the release that the fleet server approved. BaseURL needs it,
	// because a mirror path carries no version.
	Version string
	// Notes is the release note that the fleet server already has.
	Notes string
}

// Release is one release that Apply can install.
type Release struct {
	Version string `json:"version"`
	Notes   string `json:"notes,omitempty"`
	// Source is "github", "fleet" or "sideload".
	Source string `json:"source"`
	// BinaryURL, SigURL and SumsURL are the three files. A sideload leaves them
	// empty and gives Dir instead.
	BinaryURL string `json:"-"`
	SigURL    string `json:"-"`
	SumsURL   string `json:"-"`
	// Bearer is the device token of a fleet release. The release mirror of the
	// server needs it. It never leaves the host that BearerHost names.
	Bearer string `json:"-"`
	// BearerHost is the host that may see Bearer. A URL with another host gets no
	// header at all: the device token must never go to GitHub or to a redirect
	// that leaves the server.
	BearerHost string `json:"-"`
	// Dir is the directory that holds a sideloaded bundle.
	Dir string `json:"-"`
	// Size is the size of the binary when the source reports it, else 0.
	Size int64 `json:"-"`
	// PublishedAt is empty for a sideload.
	PublishedAt time.Time `json:"published_at,omitempty"`
}

// Check asks a source for the newest release.
//
// It gives ErrNoRelease when the source has nothing newer than the release that
// runs. Every other fault is an error with the reason in words: no network, a rate
// limit and a repository that does not exist must each read as themselves on the
// About page.
func (m *Manager) Check(ctx context.Context, src Source) (Release, error) {
	// A build with no public key can never install a release, so it must not
	// offer one either. A development build is in that state, and the About page
	// says so in words (internal/version.PublicKey).
	if m.opt.PublicKey == "" {
		m.setState(manifest.UpdateIdle, ErrNoKey.Error())
		return Release{}, ErrNoKey
	}
	m.setState(manifest.UpdateChecking, "")

	rel, err := m.find(ctx, src)
	if err != nil {
		m.mu.Lock()
		m.state.State = manifest.UpdateIdle
		m.state.Error = err.Error()
		m.state.Available = ""
		m.state.Source = ""
		m.offered = nil
		m.mu.Unlock()
		if !errors.Is(err, ErrNoRelease) {
			m.opt.Log("update.check.fail", err.Error())
		}
		return Release{}, err
	}

	m.mu.Lock()
	m.state.State = manifest.UpdateAvailable
	m.state.Available = rel.Version
	m.state.Source = rel.Source
	m.state.Error = ""
	copyOf := rel
	m.offered = &copyOf
	m.mu.Unlock()

	m.opt.Log("update.check", rel.Version+" is available from "+rel.Source)
	return rel, nil
}

// find gives the release of a source, or ErrNoRelease.
func (m *Manager) find(ctx context.Context, src Source) (Release, error) {
	if src.BaseURL != "" {
		return m.fleetRelease(src)
	}
	if src.Repo == "" {
		return Release{}, fmt.Errorf("no release source is set")
	}
	return m.githubRelease(ctx, src)
}

// fleetRelease builds a release from the mirror of a fleet server. The server has
// already chosen the version, so there is nothing to ask.
//
// The release mirror needs the device token on all three files, so the token goes
// into the Release with the host that may see it. A token that went anywhere else
// would give a stranger control of this device.
func (m *Manager) fleetRelease(src Source) (Release, error) {
	if src.Version == "" {
		return Release{}, fmt.Errorf("the fleet server named a release directory and no version")
	}
	if !m.newer(src.Version) {
		return Release{}, ErrNoRelease
	}
	base, err := resolveBase(src.ServerURL, src.BaseURL)
	if err != nil {
		return Release{}, err
	}
	host, err := url.Parse(base)
	if err != nil {
		return Release{}, fmt.Errorf("the release address of the fleet server is not a URL: %w", err)
	}
	asset := m.AssetName()
	return Release{
		Version:    src.Version,
		Notes:      src.Notes,
		Source:     "fleet",
		BinaryURL:  base + "/" + asset,
		SigURL:     base + "/" + asset + SigSuffix,
		SumsURL:    base + "/" + SumsName,
		Bearer:     src.Bearer,
		BearerHost: host.Host,
	}, nil
}

// resolveBase gives the whole address of a release directory.
//
// The manifest of the server carries a path, for example
// "/api/v1/releases/1.5.0". The device knows the address of its server, so the two
// make the whole address here and in no other place.
func resolveBase(serverURL, baseURL string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "", fmt.Errorf("the fleet server named no release directory")
	}
	if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
		return base, nil
	}
	server := strings.TrimSuffix(strings.TrimSpace(serverURL), "/")
	if server == "" {
		return "", fmt.Errorf("the fleet server named the release path %s and this device knows no server address", base)
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	return server + base, nil
}

// apiRelease is the part of a GitHub release document that the updater reads.
type apiRelease struct {
	TagName    string    `json:"tag_name"`
	Body       string    `json:"body"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// githubRelease asks GitHub for the newest release.
//
// The call is unauthenticated, which is why the repository must be public (D34).
// GitHub limits an unauthenticated address to 60 requests an hour, so a limit that
// the server reports is an answer in words and never a silent failure. The
// /releases/latest path already leaves out drafts and prereleases.
func (m *Manager) githubRelease(ctx context.Context, src Source) (Release, error) {
	root := src.APIRoot
	if root == "" {
		root = defaultGitHubAPI
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimSuffix(root, "/"), src.Repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := m.opt.Client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("the release page of %s is not reachable: %w", src.Repo, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("GitHub refused the check for now, because this address asked too often. Try again later.")
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("GitHub has no releases for %s", src.Repo)
	default:
		return Release{}, fmt.Errorf("the release page of %s answered %s", src.Repo, resp.Status)
	}

	var doc apiRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBytes)).Decode(&doc); err != nil {
		return Release{}, fmt.Errorf("the answer of the release page is not the JSON that this device needs: %w", err)
	}
	if doc.Draft || doc.Prerelease || doc.TagName == "" {
		return Release{}, ErrNoRelease
	}
	if !m.newer(doc.TagName) {
		return Release{}, ErrNoRelease
	}

	asset := m.AssetName()
	out := Release{
		Version:     doc.TagName,
		Notes:       strings.TrimSpace(doc.Body),
		Source:      "github",
		PublishedAt: doc.Published,
	}
	for _, a := range doc.Assets {
		switch a.Name {
		case asset:
			out.BinaryURL, out.Size = a.URL, a.Size
		case asset + SigSuffix:
			out.SigURL = a.URL
		case SumsName:
			out.SumsURL = a.URL
		}
	}
	if out.BinaryURL == "" {
		return Release{}, fmt.Errorf("%s has no %s, so it is not for this device: %w", doc.TagName, asset, ErrArch)
	}
	if out.SigURL == "" || out.SumsURL == "" {
		return Release{}, fmt.Errorf("%s has no signature or no %s, so this device refuses it", doc.TagName, SumsName)
	}
	return out, nil
}

// newer reports if a version is worth an offer: it must be different from the
// release that runs, and it must not be older.
func (m *Manager) newer(candidate string) bool {
	return CompareVersions(candidate, m.opt.Running) > 0
}
