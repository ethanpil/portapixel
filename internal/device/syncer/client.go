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

	"github.com/ethanpil/portapixel/internal/fleet"
	"github.com/ethanpil/portapixel/internal/manifest"
)

// TokenRevokedCode is the code that the fleet server sends with its 401 for a
// device token or a claim secret that it no longer accepts.
//
// The code and not the status. Any box between the device and the server can answer
// 401: a proxy with basic authentication left on, a captive portal, a web
// application firewall. Such an answer is a network fault and must not destroy a
// pairing that a person made by hand on two screens.
const TokenRevokedCode = "token-revoked"

// APIError is an answer of the fleet server that is not 200. It carries the status
// and the machine-readable code, so each caller can read the one thing it needs
// instead of matching a sentence.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Address is the address that answered, for the sentence that a person reads.
	Address string
}

func (e APIError) Error() string {
	if e.Status == http.StatusUnauthorized && e.Code != TokenRevokedCode {
		// The sentence of a 401 that did not come from the fleet API. A person who
		// reads it must look at the network and not at the pairing.
		return fmt.Sprintf("%s answered 401, and not with an answer of PortaPixel: %s",
			hostOf(e.Address), e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("%s answered %d", e.Address, e.Status)
	}
	return fmt.Sprintf("%s answered %d: %s", e.Address, e.Status, e.Message)
}

// ErrRevoked is what a caller compares against when the server refused the token of
// this device. Revoked is the test; the sentinel is here for the message.
var ErrRevoked = errors.New("the fleet server refused the device token")

// Revoked reports if an error is the one answer that ends a pairing: a 401 of the
// fleet API with the code that says the token is gone.
func Revoked(err error) bool {
	var api APIError
	if !errors.As(err, &api) {
		return false
	}
	return api.Status == http.StatusUnauthorized && api.Code == TokenRevokedCode
}

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
	address, err := fleet.ResolveURL(base, EnrollPath)
	if err != nil {
		return manifest.EnrollResponse{}, err
	}
	if err := s.call(ctx, http.MethodPost, address, "", req, &out); err != nil {
		return manifest.EnrollResponse{}, err
	}
	switch {
	case out.Status != StatusPaired && out.Status != StatusPending:
		return manifest.EnrollResponse{}, fmt.Errorf("the server answered the enrollment status %q", out.Status)
	case out.Status == StatusPaired && out.DeviceToken == "":
		return manifest.EnrollResponse{}, errors.New("the server said paired and sent no device token")
	case out.Status == StatusPending && out.ClaimSecret == "":
		// Without the claim secret the device cannot ask again, so the pairing would
		// never move on. The loop answered such a reply with another enroll at once,
		// which is a request every few milliseconds and a state file write with each
		// one.
		return manifest.EnrollResponse{}, errors.New("the server said pending and sent no claim secret")
	}
	return out, nil
}

// fetchManifest calls GET /api/v1/manifest with the device token.
func (s *Syncer) fetchManifest(ctx context.Context, base, token string) (manifest.Manifest, error) {
	address, err := fleet.ResolveURL(base, ManifestPath)
	if err != nil {
		return manifest.Manifest{}, err
	}
	var out manifest.Manifest
	if err := s.call(ctx, http.MethodGet, address, token, nil, &out); err != nil {
		return manifest.Manifest{}, err
	}
	return out, nil
}

// sendHeartbeat calls POST /api/v1/heartbeat.
func (s *Syncer) sendHeartbeat(ctx context.Context, base, token string, hb manifest.Heartbeat) error {
	address, err := fleet.ResolveURL(base, HeartbeatPath)
	if err != nil {
		return err
	}
	return s.call(ctx, http.MethodPost, address, token, hb, nil)
}

// call makes one JSON request and reads one JSON answer.
//
// Every body carries Content-Type: application/json, because the server refuses a
// body without it: a page on another site cannot then send a simple request with
// no preflight.
//
// The deadline of this one request is here and not on the HTTP client. The same
// client downloads the objects, and http.Client.Timeout covers the body read, so a
// client with one would abort every video of 1 GB (see fleet.NewClient).
func (s *Syncer) call(ctx context.Context, method, address, bearer string, body, into any) error {
	ctx, cancel := context.WithTimeout(ctx, jsonTimeout)
	defer cancel()

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
	if resp.StatusCode != http.StatusOK {
		message, code := errorText(data)
		return APIError{Status: resp.StatusCode, Code: code, Message: message, Address: address}
	}
	if readErr != nil {
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

// errorText gives the "error" and the "code" of an answer. The sentence goes into
// the ops log and into /api/status, so a person sees what the server said.
func errorText(data []byte) (message, code string) {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		return body.Error, body.Code
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 200 {
		text = text[:200]
	}
	return text, ""
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
// The manifest carries a path, for example "/api/v1/media/<sha>". fleet.ResolveURL
// holds the rule: the address must be on the paired server, and the path prefix of
// that server stays, so a server behind a reverse proxy works.
func objectURL(base, ref string) (string, error) { return fleet.ResolveURL(base, ref) }
