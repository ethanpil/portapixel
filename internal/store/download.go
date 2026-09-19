package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// errRangeRejected says that the server refused the Range request. The caller
// deletes the part file and starts again.
var errRangeRejected = errors.New("the server refused the byte range")

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

	err := fetch(ctx, client, url, bearer, part)
	if errors.Is(err, errRangeRejected) {
		// The part file does not fit this object any more. Get the whole object
		// one more time.
		os.Remove(part)
		err = fetch(ctx, client, url, bearer, part)
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
func fetch(ctx context.Context, client *http.Client, url, bearer, part string) error {
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

	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// The server continues the part file. Write at the end.
		if _, err := f.Seek(have, io.SeekStart); err != nil {
			return fmt.Errorf("seek %s: %w", part, err)
		}
	case resp.StatusCode == http.StatusOK:
		// The server sent the whole object, even if we asked for a range. Start
		// the part file again.
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

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Sync() // keep the bytes that did arrive, so that a retry can continue
		return fmt.Errorf("read the body of %s: %w", url, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", part, err)
	}
	return nil
}
