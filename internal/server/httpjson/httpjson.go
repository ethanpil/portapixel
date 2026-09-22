package httpjson

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/server/db"
)

// MaxBody is the largest JSON body that a route reads. A heartbeat with a long
// list of warnings and a playlist of a thousand items are both far under it.
const MaxBody = 1 << 20

// bodyDeadline is how long a route waits for a JSON body.
//
// The server sets no ReadTimeout, because that deadline would cut a 1 GB upload
// and a long Range download. Without any deadline a client can send the headers
// and then drip one byte of the body a minute, and the goroutine of that request
// waits for ever. This deadline covers the body of a JSON route, where a
// kilobyte over fifteen seconds is already a broken client.
const bodyDeadline = 15 * time.Second

// ContentType is the media type of every answer of this package. NotFoundJSON
// reads it to tell an answer of a handler from the text page of the router.
const ContentType = "application/json"

// Write writes one JSON answer.
func Write(w http.ResponseWriter, code int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		// A value that cannot be JSON is a fault in our own code.
		data = []byte(`{"error":"the server could not build the answer"}`)
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write(data)
}

// Error writes {"error": "..."}.
func Error(w http.ResponseWriter, code int, message string) {
	Write(w, code, map[string]string{"error": message})
}

// TokenRevokedCode is the code of the one 401 that ends a pairing: the device token
// or the claim secret of the caller is not a token of this server any more.
//
// The device needs the code, because any box between it and this server can answer
// 401 as well: a proxy with basic authentication left on, a captive portal, a web
// application firewall. Such an answer is a network fault, and a pairing that a
// person made on two screens must not go away because of one.
const TokenRevokedCode = "token-revoked"

// Revoked writes the 401 that tells a device to forget its token:
// {"error": "...", "code": "token-revoked"}.
func Revoked(w http.ResponseWriter, message string) {
	Write(w, http.StatusUnauthorized, map[string]string{
		"error": message,
		"code":  TokenRevokedCode,
	})
}

// Fields writes the 422 answer: {"error": "...", "fields": [...]}.
func Fields(w http.ResponseWriter, message string, fields db.Errors) {
	Write(w, http.StatusUnprocessableEntity, map[string]any{
		"error":  message,
		"fields": fields,
	})
}

// OK is the answer of a call that only has to work.
var OK = map[string]bool{"ok": true}

// Read reads a JSON body into into. It writes the error answer and gives false
// when the body is not the shape that the route needs.
//
// The route needs Content-Type: application/json. A form on another site can send
// a POST with text/plain, multipart/form-data or
// application/x-www-form-urlencoded and no preflight, so a route that accepted
// any type would be reachable across sites. A JSON body is not one of the three,
// and this check makes the browser ask first.
func Read(w http.ResponseWriter, r *http.Request, into any) bool {
	return read(w, r, into, false)
}

// ReadOptional is Read for a route whose fields are all optional. An empty body
// then means an empty object, so a caller that sends nothing gets the defaults.
func ReadOptional(w http.ResponseWriter, r *http.Request, into any) bool {
	return read(w, r, into, true)
}

func read(w http.ResponseWriter, r *http.Request, into any, emptyIsOK bool) bool {
	defer r.Body.Close()
	if !jsonContentType(r) {
		Error(w, http.StatusUnsupportedMediaType,
			"this route needs the header Content-Type: application/json")
		return false
	}
	// The body gets its own deadline. See bodyDeadline.
	if rc := http.NewResponseController(w); rc != nil {
		if err := rc.SetReadDeadline(time.Now().Add(bodyDeadline)); err == nil {
			defer rc.SetReadDeadline(time.Time{})
		}
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
	err := dec.Decode(into)
	if emptyIsOK && errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		Error(w, http.StatusBadRequest,
			"the request body is not the JSON that this route needs: "+err.Error())
		return false
	}
	return true
}

// jsonContentType reports if the request says that its body is JSON. A body of no
// length needs no type: a route with only optional fields takes an empty body.
func jsonContentType(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return r.ContentLength == 0
	}
	kind := strings.ToLower(strings.TrimSpace(strings.SplitN(raw, ";", 2)[0]))
	return kind == "application/json"
}

// NotFoundJSON answers every unregistered path of an API subtree with JSON.
//
// The default answer of http.ServeMux is the text/plain page "404 page not
// found", which web/shared/api.js cannot parse. One wrong path in the UI would
// then give "the answer is not JSON" instead of "there is no such route".
func NotFoundJSON(next http.Handler, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			next.ServeHTTP(w, r)
			return
		}
		rec := &jsonErrorWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
	})
}

// jsonErrorWriter turns the 404 and the 405 of http.ServeMux into JSON. Every
// other answer goes through as it is.
type jsonErrorWriter struct {
	http.ResponseWriter
	replaced bool
	done     bool
}

func (j *jsonErrorWriter) WriteHeader(code int) {
	if j.done {
		return
	}
	j.done = true
	// A handler that answered in JSON already said what it had to say. Only the
	// text page of the router is replaced.
	//
	// Without this test, "the media store holds no file for this object" became
	// "this server has no route with this path". The device then put that wrong
	// sentence into sync_error and into the ops log of the operator.
	if j.Header().Get("Content-Type") == ContentType {
		j.ResponseWriter.WriteHeader(code)
		return
	}
	switch code {
	case http.StatusNotFound:
		j.replaced = true
		Error(j.ResponseWriter, code, "this server has no route with this path")
	case http.StatusMethodNotAllowed:
		j.replaced = true
		Error(j.ResponseWriter, code, "this route does not take this method")
	default:
		j.ResponseWriter.WriteHeader(code)
	}
}

func (j *jsonErrorWriter) Write(b []byte) (int, error) {
	if !j.done {
		j.WriteHeader(http.StatusOK)
	}
	if j.replaced {
		// The body of the answer that we replaced goes nowhere.
		return len(b), nil
	}
	return j.ResponseWriter.Write(b)
}

// Unwrap gives the writer below, so http.NewResponseController and the flush of a
// large answer still reach the real connection.
func (j *jsonErrorWriter) Unwrap() http.ResponseWriter { return j.ResponseWriter }

// ReadFrom passes a body straight to the writer below.
//
// http.ServeContent copies with io.CopyN, which takes the io.ReaderFrom of the real
// writer of net/http when it is there. On Linux that path is sendfile: the kernel
// moves the file to the socket with no buffer of ours in between. A wrapper that
// only carries Write hides the interface, and every media download and every release
// binary then went through a 32 KiB loop in user space. io.Copy does not look at
// Unwrap, so the delegation is written here.
func (j *jsonErrorWriter) ReadFrom(src io.Reader) (int64, error) {
	if !j.done {
		j.WriteHeader(http.StatusOK)
	}
	if j.replaced {
		// The body of the answer that we replaced goes nowhere.
		return io.Copy(io.Discard, src)
	}
	if rf, ok := j.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(j.ResponseWriter, src)
}
