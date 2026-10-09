package player

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The fake mpv.
//
// The test binary is the fake: TestMain looks for fakeFlag in the arguments and
// then runs fakeMPV in place of the tests. The same trick works on Windows and on
// Linux, and it needs no compiler and no mpv. The fake listens on the socket of
// --input-ipc-server and speaks the JSON IPC of mpv for the commands that the
// supervisor sends.
//
// A test drives the fake over a second connection with commands that mpv does
// not have:
//
//	fake-next           the item on the screen ends; the next one shows
//	fake-hang           answer nothing more, as an mpv after SIGSTOP
//	fake-stall          time-pos stops
//	fake-exit <code>    end the process
//	fake-drops <vo> <decoder>  set the two dropped frame counters
//	fake-fault <text>   set user-data/pptr/fault, as transitions.lua does
//	fake-dump           give the arguments, the playlist, the load count and
//	                    the values that set_property set
//
// set_property keeps the value and tells the observers, so a test can also set
// user-data/pptr/moved as transitions.lua does.
const fakeFlag = "--pp-fake-mpv"

// fakeLife is how long the fake lives at most. A test that fails must not leave
// a process for ever.
const fakeLife = 60 * time.Second

// fakeHwdec is the decoder that the fake reports for a video.
const fakeHwdec = "fake-hwdec"

type fakeEntry struct {
	ID   int64             `json:"id"`
	Path string            `json:"path"`
	Opts map[string]string `json:"opts"`
}

type fakeClient struct {
	conn    net.Conn
	writeMu sync.Mutex
	// observed maps a property name to the IDs of its observations.
	observed map[string][]int64
}

type fakeMPV struct {
	mu      sync.Mutex
	clients []*fakeClient
	list    []fakeEntry
	pos     int // -1 when idle
	nextID  int64
	loads   int
	// loading counts the loads with replace, so a late start of an old list does
	// nothing.
	loading int
	timePos float64
	stall   bool
	hang    bool
	vo, dec int
	fault   any
	// props holds the values of set_property, for example user-data/pptr/motion.
	props map[string]any
}

// runFakeMPV is the main function of the fake. It gives the exit code.
func runFakeMPV(args []string) int {
	socket := ""
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--input-ipc-server="); ok {
			socket = v
		}
	}
	if socket == "" {
		return 2
	}
	os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return 3
	}
	go func() {
		time.Sleep(fakeLife)
		os.Exit(4)
	}()
	f := &fakeMPV{pos: -1, props: map[string]any{}}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 5
		}
		c := &fakeClient{conn: conn, observed: map[string][]int64{}}
		f.mu.Lock()
		f.clients = append(f.clients, c)
		f.mu.Unlock()
		go f.serve(c, args)
	}
}

func (f *fakeMPV) serve(c *fakeClient, args []string) {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var req struct {
			Command   []any `json:"command"`
			RequestID int64 `json:"request_id"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.Command) == 0 {
			continue
		}
		name, _ := req.Command[0].(string)
		f.mu.Lock()
		if f.hang && !strings.HasPrefix(name, "fake-") {
			f.mu.Unlock()
			continue
		}
		f.handle(c, name, req.Command[1:], req.RequestID, args)
		f.mu.Unlock()
	}
}

// handle runs one command. The caller holds f.mu.
func (f *fakeMPV) handle(c *fakeClient, name string, a []any, id int64, args []string) {
	reply := func(data any, errText string) {
		c.send(map[string]any{"request_id": id, "error": errText, "data": data})
	}
	switch name {
	case "observe_property":
		obs, _ := a[0].(float64)
		prop, _ := a[1].(string)
		c.observed[prop] = append(c.observed[prop], int64(obs))
		reply(nil, "success")
		c.send(map[string]any{"event": "property-change", "id": int64(obs), "name": prop, "data": f.value(prop)})
	case "get_property":
		prop, _ := a[0].(string)
		switch v := f.value(prop); {
		case prop == "time-pos" && f.pos < 0:
			reply(nil, "property unavailable")
		case v == nil && prop != "hwdec-current":
			reply(nil, "property not found")
		default:
			reply(v, "success")
		}
		if prop == "time-pos" && !f.stall && f.pos >= 0 {
			f.timePos++
		}
	case "loadfile":
		path, _ := a[0].(string)
		mode, _ := a[1].(string)
		opts := map[string]string{}
		if len(a) > 3 {
			if m, ok := a[3].(map[string]any); ok {
				for k, v := range m {
					opts[k], _ = v.(string)
				}
			}
		}
		f.nextID++
		f.loads++
		entry := fakeEntry{ID: f.nextID, Path: path, Opts: opts}
		reply(map[string]any{"playlist_entry_id": entry.ID}, "success")
		if mode == "replace" {
			if f.pos >= 0 {
				f.broadcast(map[string]any{"event": "end-file", "reason": "stop", "playlist_entry_id": f.list[f.pos].ID})
			}
			f.list = []fakeEntry{entry}
			f.pos = 0
			// mpv opens the new file in its play loop, after it took the commands
			// that came with this one. The appends of the list come first.
			f.loading++
			gen := f.loading
			time.AfterFunc(10*time.Millisecond, func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.loading == gen && !f.hang {
					f.play(0)
				}
			})
			return
		}
		f.list = append(f.list, entry)
	case "fake-next":
		reply(nil, "success")
		if f.pos >= 0 {
			f.broadcast(map[string]any{"event": "end-file", "reason": "eof", "playlist_entry_id": f.list[f.pos].ID})
			f.play((f.pos + 1) % len(f.list))
		}
	case "fake-hang":
		f.hang = true
		reply(nil, "success")
	case "fake-stall":
		f.stall = true
		reply(nil, "success")
	case "fake-exit":
		code, _ := a[0].(float64)
		os.Exit(int(code))
	case "fake-drops":
		vo, _ := a[0].(float64)
		dec, _ := a[1].(float64)
		f.vo, f.dec = int(vo), int(dec)
		reply(nil, "success")
	case "fake-fault":
		f.fault = a[0]
		reply(nil, "success")
		f.notify("user-data/pptr/fault")
	case "set_property":
		prop, _ := a[0].(string)
		f.props[prop] = a[1]
		reply(nil, "success")
		f.notify(prop)
	case "fake-dump":
		reply(map[string]any{"args": args, "list": f.list, "loads": f.loads, "pos": f.pos, "props": f.props}, "success")
	case "quit":
		reply(nil, "success")
		os.Exit(0)
	default:
		reply(nil, "success")
	}
}

// play starts the entry at pos, as mpv does after a load or at the end of a
// file. A path with "broken" in it fails to load; when every entry fails, mpv
// goes idle.
func (f *fakeMPV) play(pos int) {
	for tries := 0; tries < len(f.list); tries++ {
		e := f.list[pos]
		f.pos = pos
		f.broadcast(map[string]any{"event": "start-file", "playlist_entry_id": e.ID})
		if !strings.Contains(e.Path, "broken") {
			f.timePos = 0
			f.notify("hwdec-current")
			f.broadcast(map[string]any{"event": "playback-restart"})
			return
		}
		f.broadcast(map[string]any{"event": "end-file", "reason": "error", "playlist_entry_id": e.ID,
			"file_error": "loading failed"})
		pos = (pos + 1) % len(f.list)
	}
	f.pos = -1
	f.notify("hwdec-current")
	f.notify("idle-active")
}

// value gives the value of a property, or nil.
func (f *fakeMPV) value(prop string) any {
	switch prop {
	case "idle-active":
		return f.pos < 0
	case "playlist-pos":
		return f.pos
	case "time-pos":
		if f.pos >= 0 && isImagePath(f.list[f.pos].Path) {
			return 0.0
		}
		return f.timePos
	case "frame-drop-count":
		return f.vo
	case "decoder-frame-drop-count":
		return f.dec
	case "hwdec-current":
		switch {
		case f.pos < 0:
			return nil
		case isImagePath(f.list[f.pos].Path):
			return "no"
		}
		return fakeHwdec
	case "user-data/pptr/fault":
		return f.fault
	}
	return f.props[prop]
}

func isImagePath(p string) bool {
	return slices.ContainsFunc([]string{".png", ".jpg"}, func(ext string) bool { return strings.HasSuffix(p, ext) })
}

// notify sends a property-change event to each client that observes prop.
func (f *fakeMPV) notify(prop string) {
	for _, c := range f.clients {
		for _, id := range c.observed[prop] {
			c.send(map[string]any{"event": "property-change", "id": id, "name": prop, "data": f.value(prop)})
		}
	}
}

func (f *fakeMPV) broadcast(m map[string]any) {
	for _, c := range f.clients {
		c.send(m)
	}
}

func (c *fakeClient) send(m map[string]any) {
	line, _ := json.Marshal(m)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	c.conn.Write(append(line, '\n'))
}
