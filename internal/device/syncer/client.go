package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethanpil/portapixel/internal/manifest"
)

// ErrRevoked says that the server refused the device token. The device then drops
// to the unpaired state and pairs again with the token of the TOML, if there is
// one.
var ErrRevoked = errors.New("the fleet server refused the device token")

// maxBody is the largest answer that this package reads. A manifest of a thousand
// objects is far under it, and a body with no end must not fill the memory of a
// device with 512 MB.
const maxBody = 8 << 20

// enroll calls POST /api/v1/enroll. token is an enrollment token, a device token,
// a claim secret, or an empty value to ask for a pairing code (D25).
//
// This call and the heartbeat are the only two that carry the full hardware ID. It
// is the secret that permits a re-pair with the fleet enrollment token, so it
// leaves the device in no other place.
func (s *Syncer) enroll(ctx context.Context, base, token string) (manifest.EnrollResponse, error) {
	req := manifest.EnrollRequest{
		DeviceID:   s.opt.Identity.DeviceID,
		HardwareID: s.opt.Identity.HardwareID,
		Name:       s.opt.Config().Device.Name,
		Token:      token,
		Version:    s.opt.Version,
	}
	var out manifest.EnrollResponse
	if err := s.call(ctx, http.MethodPost, base+EnrollPath, "", req, &out); err != nil {
		return manifest.EnrollResponse{}, err
	}
	if out.Status != "paired" && out.Status != "pending" {
		return manifest.EnrollResponse{}, fmt.Errorf("the server answered the enrollment status %q", out.Status)
	}
	if out.Status == "paired" && out.DeviceToken == "" {
		return manifest.EnrollResponse{}, errors.New("the server said paired and sent no device token")
	}
	return out, nil
}

// fetchManifest calls GET /api/v1/manifest with the device token.
func (s *Syncer) fetchManifest(ctx context.Context, base, token string) (manifest.Manifest, error) {
	var out manifest.Manifest
	if err := s.call(ctx, http.MethodGet, base+ManifestPath, token, nil, &out); err != nil {
		return manifest.Manifest{}, err
	}
	return out, nil
}

// sendHeartbeat calls POST /api/v1/heartbeat.
func (s *Syncer) sendHeartbeat(ctx context.Context, base, token string, hb manifest.Heartbeat) error {
	return s.call(ctx, http.MethodPost, base+HeartbeatPath, token, hb, nil)
}

// call makes one JSON request and reads one JSON answer.
//
// Every body carries Content-Type: application/json, because the server refuses a
// body without it: a page on another site cannot then send a simple request with
// no preflight.
func (s *Syncer) call(ctx context.Context, method, address, bearer string, body, into any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return fmt.Errorf("make the request for %s: %w", address, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := s.opt.Client.Do(req)
	if err != nil {
		return fmt.Errorf("%s is not reachable: %w", hostOf(address), err)
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrRevoked
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s answered %s: %s", address, resp.Status, errorText(data))
	case readErr != nil:
		return fmt.Errorf("read the answer of %s: %w", address, readErr)
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("the answer of %s is not the JSON that this device needs: %w", address, err)
	}
	return nil
}

// errorText gives the "error" field of an answer, or the first part of the body.
// The sentence goes into the ops log and into /api/status, so a person sees what
// the server said.
func errorText(data []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		return body.Error
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// hostOf gives the host of an address, for a message that a person reads.
func hostOf(address string) string {
	if u, err := url.Parse(address); err == nil && u.Host != "" {
		return u.Host
	}
	return address
}

// objectURL gives the whole address of one object of the manifest.
//
// The manifest of the server carries a path, for example
// "/api/v1/media/<sha>". A whole address is permitted, and it must name the host
// of the server: a manifest is not trusted input, and a device that follows an
// address to another host would carry its token there and take content from a
// stranger.
func objectURL(base, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("the manifest names an object with no address")
	}
	root, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("the server address is not a URL: %w", err)
	}
	target, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%q is not a URL", ref)
	}
	out := root.ResolveReference(target)
	if out.Scheme != root.Scheme || out.Host != root.Host {
		return "", fmt.Errorf("%q is not on %s", ref, root.Host)
	}
	return out.String(), nil
}
