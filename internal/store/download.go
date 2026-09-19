package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// errRangeRejected says that the server refused the Range request. The caller
// deletes the part file and starts again.
var errRangeRejected = errors.New("the server refused the byte range")

// MaxUnknownBytes is the largest download that a caller may take when it does not
// know the size. A release binary is about 25 MB and the fleet release mirror
// names no size, so the value is generous.
//
// Without it a body with no end fills the partition, and a device that filled its
// own partition can write no configuration, no state file and no log line.
const MaxUnknownBytes = 512 << 20

// idleLimit is how long a download may make no progress before it stops.
//
// A limit on the whole request is wrong here: a video of 1 GB on a slow link needs
// many minutes and is not a fault. A connection that stopped sending is a fault,
// and the part file keeps the bytes that did arrive, so the next round continues
// where this one stopped (D24). A test lowers the value.
var idleLimit = 60 * time.Second

// Download fetches url into destPath and checks it.
//
// The bytes go to destPath+".part" first. A .part file that is already there
// continues with a "Range: bytes=N-" request, so a dropped 1 GB video does not
// start from zero (D24). Download renames the file to destPath only after the
// SHA-256 matches wantSHA. On a hash failure it deletes the part file, because
// the content is wrong and a resume would keep the wrong bytes.
//
// wantSize may be 0 when the caller does not know the size.
func Download(ctx context.Context, client *http.Client, url, bearer, destPath, wantSHA string, wantSize int64) error {
	if wantSHA == "" {
		return errors.New("download needs the wanted SHA-256")
	}
	part := destPath + ".part"

	err := fetch(ctx, client, url, bearer, part, wantSize)
	if errors.Is(err, errRangeRejected) {
		// The part file does not fit this object any more. Get the whole object
		// one more time.
		os.Remove(part)
		err = fetch(ctx, client, url, bearer, part, wantSize)
	}
	if err != nil {
		return err
	}

	info, err := os.Stat(part)
	if err != nil {
		return fmt.Errorf("stat %s: %w", part, err)
	}
	if wantSize > 0 && info.Size() != wantSize {
		if info.Size() > wantSize {
			// More bytes than the manifest promises. The part file is wrong.
			os.Remove(part)
		}
		return fmt.Errorf("%s has %d bytes, want %d", part, info.Size(), wantSize)
	}

	got, err := HashFile(part)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, wantSHA) {
		os.Remove(part)
		return fmt.Errorf("%s has SHA-256 %s, want %s", part, got, wantSHA)
	}

	if err := os.Rename(part, destPath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", part, destPath, err)
	}
	fsutil.SyncDir(filepath.Dir(destPath))
	return nil
}

// fetch writes the body of url to the part file. It continues a part file that
// is already there. It syncs the part file before it returns, so that the bytes
// survive a power cut and a later call can continue them.
//
// wantSize is the size that the manifest promises, or 0 when the caller does not
// know it. fetch never writes more than that number of bytes.
func fetch(ctx context.Context, client *http.Client, url, bearer, part string, wantSize int64) error {
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", part, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", part, err)
	}
	have := info.Size()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("make the request for %s: %w", url, err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	// offset is the place in the part file where the body goes.
	offset := have
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// The server continues the part file. Write at the end, but only if the
		// answer begins at the byte that we asked for. A cache or a proxy can
		// answer 206 from another offset, and those bytes at the end of the part
		// file would make an object that is wrong in the middle.
		if start, ok := rangeStart(resp.Header.Get("Content-Range")); !ok || start != have {
			return errRangeRejected
		}
		if _, err := f.Seek(have, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s: %w", part, err)
		}
	case resp.StatusCode == http.StatusOK:
		// The server sent the whole object, even if we asked for a range. Start
		// the part file again.
		offset = 0
		if err := f.Truncate(0); err != nil {
			return fmt.Errorf("truncate %s: %w", part, err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s: %w", part, err)
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		return errRangeRejected
	default:
		return fmt.Errorf("get %s: the server answered %s", url, resp.Status)
	}

	// Take the promised number of bytes and no more. A server that sends more
	// than the manifest promises must not be able to fill the media partition:
	// the card would then hold a part file that no later download can remove.
	//
	// A caller that knows no size gets the ceiling instead of no limit at all.
	limit := wantSize - offset
	if wantSize <= 0 {
		limit = MaxUnknownBytes - offset
	}
	if limit < 0 {
		limit = 0
	}
	body := io.LimitReader(resp.Body, limit)

	// A read that makes no progress ends the copy. Without it a connection that
	// stopped in the middle holds the round until the context of the caller ends.
	// That is half an hour on a device in the middle of a sync.
	guard := time.AfterFunc(idleLimit, func() { resp.Body.Close() })
	defer guard.Stop()
	if _, err := io.Copy(f, &progressReader{r: body, guard: guard}); err != nil {
		f.Sync() // keep the bytes that did arrive, so that a retry can continue
		return fmt.Errorf("read the body of %s: %w", url, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", part, err)
	}
	return nil
}

// progressReader puts the idle guard forward at every block that arrives. io.Copy
// reads in blocks of 32 kB, so a download that moves at all keeps the guard back.
type progressReader struct {
	r     io.Reader
	guard *time.Timer
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.guard.Reset(idleLimit)
	}
	return n, err
}

// rangeStart gives the first byte number of a Content-Range header value, for
// example 600 from "bytes 600-999/1000". It reports false when the value is
// missing or has another shape.
func rangeStart(value string) (int64, bool) {
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "bytes"))
	dash := strings.IndexByte(v, '-')
	if dash < 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(v[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}
