package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethanpil/portapixel/internal/fsutil"
)

// Reserve is the free space that the store keeps back. An upload that would take
// the disk below it is refused before it starts.
//
// The server has a database, a write-ahead log and the release mirror on the
// same disk. A full disk stops the whole fleet, so the store gives up an upload
// long before that.
const Reserve = 512 << 20

// ErrNoSpace says that the upload does not fit.
var ErrNoSpace = errors.New("there is not enough free space for this file")

// ErrTooLarge says that the body is longer than the limit of the store.
var ErrTooLarge = errors.New("this file is longer than the limit of the server")

// Store is the media store under one directory.
type Store struct {
	// root holds the objects at <root>/<first two hex>/<sha>.
	root string
	// thumbs holds the thumbnails at <thumbs>/<sha>.jpg.
	thumbs string
	// MaxBytes is the largest object that the store takes. Zero means "only the
	// free space decides".
	MaxBytes int64
}

// New makes the directories of the store and gives it.
func New(dataDir string) (*Store, error) {
	s := &Store{
		root:   filepath.Join(dataDir, "media"),
		thumbs: filepath.Join(dataDir, "thumbs"),
	}
	for _, dir := range []string{s.root, s.thumbs} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("make %s: %w", dir, err)
		}
	}
	return s, nil
}

// Root gives the object directory. The health page probes it for writability.
func (s *Store) Root() string { return s.root }

// Path gives the file name of one object. The caller checks the hash first with
// db.ValidSHA256: a value from a request must never reach a file path.
func (s *Store) Path(sha string) string {
	return filepath.Join(s.root, sha[:2], sha)
}

// ThumbPath gives the file name of one thumbnail.
func (s *Store) ThumbPath(sha string) string {
	return filepath.Join(s.thumbs, sha+".jpg")
}

// Has reports if the store holds this object.
func (s *Store) Has(sha string) bool {
	info, err := os.Stat(s.Path(sha))
	return err == nil && !info.IsDir()
}

// Result says what an upload made.
type Result struct {
	SHA256 string
	Size   int64
	MIME   string
	// Duplicate is true when the store already held these bytes.
	Duplicate bool
	// Width, Height and HasThumb come from the thumbnail step. They stay zero
	// for a file that is not an image that we can decode.
	Width    int
	Height   int
	HasThumb bool
}

// Put streams body into the store and gives what it made.
//
// The bytes go to a temporary file in the directory of the object, so the rename
// at the end cannot cross a filesystem. The hash grows while the bytes arrive,
// so the file is never read a second time. The name of the object comes from
// those bytes and from nothing that the client said, which is what makes the
// store content-addressed.
//
// declaredSize is the Content-Length of the request, or 0 when it is not known.
// Put uses it only for the free-space check.
func (s *Store) Put(body io.Reader, origName string, declaredSize int64) (Result, error) {
	if s.MaxBytes > 0 && declaredSize > s.MaxBytes {
		return Result{}, ErrTooLarge
	}
	if err := s.checkSpace(declaredSize); err != nil {
		return Result{}, err
	}

	// The temporary file goes in a shard directory. Every object of that shard
	// lives there, so the rename stays inside one filesystem.
	tmpDir := filepath.Join(s.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("make %s: %w", tmpDir, err)
	}
	tmp, err := os.CreateTemp(tmpDir, "upload*")
	if err != nil {
		return Result{}, fmt.Errorf("make a temporary file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		tmp.Close()
		if !committed {
			os.Remove(tmpName)
		}
	}()

	hash := sha256.New()
	var reader io.Reader = body
	if s.MaxBytes > 0 {
		// One byte more than the limit, so a body that gives no Content-Length
		// and then sends too much is caught as well.
		reader = io.LimitReader(body, s.MaxBytes+1)
	}
	size, err := io.Copy(io.MultiWriter(tmp, hash), reader)
	if err != nil {
		return Result{}, fmt.Errorf("read the upload: %w", err)
	}
	if s.MaxBytes > 0 && size > s.MaxBytes {
		return Result{}, ErrTooLarge
	}
	if size == 0 {
		return Result{}, errors.New("the upload has no bytes in it")
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, fmt.Errorf("sync the upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("close the upload: %w", err)
	}

	sha := hex.EncodeToString(hash.Sum(nil))
	res := Result{SHA256: sha, Size: size, MIME: TypeOf(origName, tmpName)}

	dest := s.Path(sha)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Result{}, fmt.Errorf("make %s: %w", filepath.Dir(dest), err)
	}
	if s.Has(sha) {
		// The store already holds these bytes. Keep the copy that is there: it is
		// the same file, and a rename over it would only risk the good copy.
		res.Duplicate = true
		os.Remove(tmpName)
		committed = true
	} else {
		if err := os.Rename(tmpName, dest); err != nil {
			return Result{}, fmt.Errorf("rename the upload to %s: %w", dest, err)
		}
		committed = true
		fsutil.SyncDir(filepath.Dir(dest))
	}

	// The thumbnail is a convenience. A failure here is not a failed upload: the
	// UI shows its own icon when there is no thumbnail. The size of the picture
	// comes back even then, because the header gives it and the media page shows
	// it.
	res.Width, res.Height, res.HasThumb = s.makeThumb(dest, sha)
	return res, nil
}

// Delete removes one object and its thumbnail. The caller checks first that no
// playlist holds the object.
func (s *Store) Delete(sha string) error {
	if err := os.Remove(s.Path(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(s.ThumbPath(sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// checkSpace refuses an upload that would leave less than Reserve free.
func (s *Store) checkSpace(size int64) error {
	free, err := fsutil.FreeBytes(s.root)
	if err != nil {
		// The free space is not known on every system. An upload must not fail
		// because of that; the filesystem still gives an error if it fills up.
		return nil
	}
	if free < uint64(size)+Reserve {
		return fmt.Errorf("%w: the file needs %d bytes and the disk has %d free, less the reserve of %d",
			ErrNoSpace, size, free, Reserve)
	}
	return nil
}

// TypeOf says what media type an object holds. The extension answers first,
// because it is what the player uses to choose an image element or a video
// element. A name with no useful extension gets the answer of http.DetectContentType,
// which reads the first bytes of the file.
func TypeOf(origName, path string) string {
	if ext := strings.ToLower(filepath.Ext(origName)); ext != "" {
		if kind := mime.TypeByExtension(ext); kind != "" {
			return kind
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()

	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(head[:n])
}
