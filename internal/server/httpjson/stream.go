package httpjson

import (
	"io"
	"net/http"
	"time"
)

// idleGrace is how long a streamed upload may send nothing before the server
// gives up on it.
//
// A JSON route gets one deadline for the whole body (see bodyDeadline). An upload
// of a 1 GB video cannot work that way, and a blanket ReadTimeout on the server
// would cut it. So the deadline moves: every block of bytes that arrives buys
// another minute. A live upload passes at any speed, and a connection that sends
// nothing at all ends.
const idleGrace = 60 * time.Second

// StreamBody gives the body of a streamed upload with an idle guard on it.
//
// The caller reads the result instead of r.Body. A read that returns bytes moves
// the read deadline forward; a stall of idleGrace ends the read with an error, and
// the route answers 400.
func StreamBody(w http.ResponseWriter, r *http.Request) io.Reader {
	rc := http.NewResponseController(w)
	if rc == nil {
		return r.Body
	}
	if err := rc.SetReadDeadline(time.Now().Add(idleGrace)); err != nil {
		// The writer does not carry a deadline, for example in a test that uses
		// its own recorder. Then there is nothing to move.
		return r.Body
	}
	return &idleReader{src: r.Body, rc: rc}
}

// idleReader moves the read deadline forward on each read that gives bytes.
type idleReader struct {
	src io.Reader
	rc  *http.ResponseController
	// lastMove is when the deadline moved. One move a second is enough, and it
	// keeps a syscall off every 32 kB block of a large upload.
	lastMove time.Time
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.src.Read(p)
	if n > 0 {
		if now := time.Now(); now.Sub(i.lastMove) >= time.Second {
			i.lastMove = now
			i.rc.SetReadDeadline(now.Add(idleGrace))
		}
	}
	return n, err
}
