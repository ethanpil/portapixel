package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// client speaks the admin API with a session cookie, the same way the browser
// does. Every call that changes state carries the X-PortaPixel header.
type client struct {
	base string
	http *http.Client
}

func newClient(base string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}, nil
}

// call sends a JSON body and reads a JSON answer. `out` may be nil.
func (c *client) call(method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		req.Header.Set("X-PortaPixel", "1")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	return c.finish(req, path, out)
}

// put sends raw bytes as the body, for an upload. The name goes in X-Filename.
func (c *client) put(path, name string, data []byte, out any) error {
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("X-PortaPixel", "1")
	req.Header.Set("X-Filename", url.QueryEscape(name))
	req.Header.Set("Content-Type", "application/octet-stream")
	return c.finish(req, path, out)
}

// bearer sends a device call with a device token.
func (c *client) bearer(method, path, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.finish(req, path, out)
}

func (c *client) finish(req *http.Request, path string, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", req.Method, path, res.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}
