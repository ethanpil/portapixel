package player

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// The limits of the IPC connection.
const (
	// dialTimeout is how long one connect to the socket may take. The socket is
	// local, so a connect answers at once or fails at once.
	dialTimeout = time.Second
	// writeTimeout is how long one command may take to leave. mpv reads its
	// socket in a thread of its own, so a write waits only when mpv is frozen.
	writeTimeout = 2 * time.Second
	// maxLine is the longest line that we read from mpv. Our requests get short
	// answers; a longer line is a fault, and the scanner then ends the read.
	maxLine = 1 << 20
	// msgQueue is how many messages from mpv may wait for the loop.
	msgQueue = 64
)

// message is one line from mpv: the reply to a request, or an event. A reply has
// no "event" field (ARCHITECTURE section 7).
type message struct {
	RequestID int64           `json:"request_id"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`

	Event string `json:"event"`
	// ID and Name belong to the event "property-change".
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Reason, FileError and EntryID belong to the event "end-file".
	Reason    string `json:"reason"`
	FileError string `json:"file_error"`
	EntryID   int64  `json:"playlist_entry_id"`
}

// ipcConn is one connection to the IPC socket of mpv. It uses the standard
// library only: the protocol is one JSON object on each line.
type ipcConn struct {
	conn net.Conn
	// msgs takes each message from mpv. It is closed when the connection ends.
	msgs chan message
	done chan struct{}
	once sync.Once
	next int64
	// broken is the error of the first write that failed. A write that timed out
	// can have sent a part of its line, and an mpv that does not read makes each
	// write wait for writeTimeout. So each send after it fails at once. Only the
	// loop sends, so it needs no lock.
	broken error
}

// dialIPC connects to the socket of mpv.
func dialIPC(path string) (*ipcConn, error) {
	conn, err := net.DialTimeout("unix", path, dialTimeout)
	if err != nil {
		return nil, err
	}
	c := &ipcConn{conn: conn, msgs: make(chan message, msgQueue), done: make(chan struct{})}
	go c.read()
	return c, nil
}

// read gives each line from mpv to the loop. A line that is not JSON is skipped:
// a fault in one line must not end the control of the player.
func (c *ipcConn) read() {
	defer close(c.msgs)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		select {
		case c.msgs <- m:
		case <-c.done:
			return
		}
	}
}

// send writes one command and gives its request ID. mpv answers each request
// in the order of the requests.
func (c *ipcConn) send(args ...any) (int64, error) {
	c.next++
	if c.broken != nil {
		return 0, c.broken
	}
	line, err := json.Marshal(struct {
		Command   []any `json:"command"`
		RequestID int64 `json:"request_id"`
	}{args, c.next})
	if err != nil {
		return 0, err
	}
	c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		c.broken = err
		return 0, err
	}
	return c.next, nil
}

// close ends the connection and the reader.
func (c *ipcConn) close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}
