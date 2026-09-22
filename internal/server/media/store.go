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
	"syscall"
	"time"

	"github.com/ethanpil/portapixel/internal/fsutil"
	"github.com/ethanpil/portapixel/internal/playlist"
	"github.com/ethanpil/portapixel/internal/store"
)

// Reserve is the free space that the store keeps back. An upload that would take
// the disk below it is refused.
//
// The server has a database, a write-ahead log and the release mirror on the same
// disk. A full disk stops the whole fleet, so the store gives up an upload long
// before that.
const Reserve = 512 << 20

// ErrNoSpace says that the upload does not fit.
var ErrNoSpace = errors.New("there is not enough free space for this file")

// ErrBadHash says that a value is not a SHA-256 in the form that the store uses.
var ErrBadHash = errors.New("that is not a SHA-256 value of 64 lower case hex characters")

// isNoSpace reports if err says that the filesystem is full.
//
// It reads the error of the standard library and not a string: the wording of a
// write error belongs to the operating system. fs.ErrNoSpace does not exist, so the
// test is against the syscall value that every system that we run on gives.
func isNoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) }

// tmpDirName is the directory of the part files of an upload, under the object
// root. Every object lives under the same root, so the rename at the end of an
// upload stays inside one filesystem.
const tmpDirName = "tmp"

// Store is the media store under one directory.
type Store struct {
	// root holds the objects at <root>/<first two hex>/<sha>.
	root string
	// thumbs holds the thumbnails at <thumbs>/<sha>.jpg.
	thumbs string
}

// New makes the directories of the store and gives it.
func New(dataDir string) (*Store, error) {
	s := &Store{
		root:   filepath.Join(dataDir, "media"),
		thumbs: filepath.Join(dataDir, "thumbs"),
	}
	for _, dir := range []string{s.root, s.thumbs, filepath.Join(s.root, tmpDirName)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("make %s: %w", dir, err)
		}
	}
	return s, nil
}

// Root gives the object directory. The health page probes it for writability.
func (s *Store) Root() string { return s.root }

// Thumbs gives the thumbnail directory.
func (s *Store) Thumbs() string { return s.thumbs }

// Path gives the file name of one object.
//
// The store checks the hash itself. A value from a URL path becomes a file name
// here, so a caller that forgot the check must not be able to reach a file, and a
// short value must not be able to make this function read out of range.
func (s *Store) Path(sha string) (string, error) {
	if !store.IsSHA256(sha) {
		return "", ErrBadHash
	}
	return filepath.Join(s.root, sha[:2], sha), nil
}

// ThumbPath gives the file name of one thumbnail.
func (s *Store) ThumbPath(sha string) (string, error) {
	if !store.IsSHA256(sha) {
		return "", ErrBadHash
	}
	return filepath.Join(s.thumbs, sha+".jpg"), nil
}

// Has reports if the store holds this object. A hash of the wrong shape gives
// false: nothing of that name can be in the store.
func (s *Store) Has(sha string) bool {
	path, err := s.Path(sha)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
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
// at the end cannot cross a filesystem. The hash grows while the bytes arrive, so
// the file is never read a second time. The name of the object comes from those
// bytes and from nothing that the client said, which is what makes the store
// content-addressed.
//
// declaredSize is the Content-Length of the request. It is negative when the
// length is not known, which is what a chunked body gives. Then the only limit is
// the free space.
//
// There is no configured maximum size. The free space less Reserve is the limit,
// which is what D27 says: "uploads are streamed and limited only by disk". The
// body always goes through a reader with that limit on it, so a body that lies
// about its length, or that declares nothing at all, stops at the same place.
func (s *Store) Put(body io.Reader, origName string, declaredSize int64) (Result, error) {
	room, known, err := s.room()
	if err != nil {
		return Result{}, err
	}
	if known {
		if declaredSize > 0 && declaredSize > room {
			return Result{}, fmt.Errorf("%w: the file declares %d bytes and the disk has %d free, less the reserve of %d",
				ErrNoSpace, declaredSize, room+Reserve, Reserve)
		}
	}

	tmpDir := filepath.Join(s.root, tmpDirName)
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
			// A failed upload takes its part file with it. Without this a disk
			// that filled up would stay full of the attempt that filled it.
			os.Remove(tmpName)
		}
	}()

	hash := sha256.New()
	var reader io.Reader = body
	if known {
		// One byte more than the room, so a body that goes over it is an error
		// and not a file that got cut.
		reader = io.LimitReader(body, room+1)
	}
	size, err := io.Copy(io.MultiWriter(tmp, hash), reader)
	if err != nil {
		if isNoSpace(err) {
			// The filesystem filled up while the bytes arrived. That is the same answer
			// as a file that does not fit the reserve, and not "the body did not
			// arrive": the admin must read "there is no room" and not "try it again".
			return Result{}, fmt.Errorf("%w: the disk filled up while the file arrived", ErrNoSpace)
		}
		return Result{}, fmt.Errorf("read the upload: %w", err)
	}
	if known && size > room {
		return Result{}, fmt.Errorf("%w: the upload passed the %d bytes that the disk has free, less the reserve of %d",
			ErrNoSpace, room, Reserve)
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

	dest, err := s.Path(sha)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Result{}, fmt.Errorf("make %s: %w", filepath.Dir(dest), err)
	}
	if s.Has(sha) {
		// The store already holds these bytes. Keep the copy that is there: it is
		// the same file, and a rename over it would only risk the good copy.
		res.Duplicate = true
		os.Remove(tmpName)
		committed = true
		// The object is referenced again, so it gets a new modification time. The
		// orphan sweep keeps a young file whatever the database said when the sweep
		// started, and without this the sweep could remove the object between this
		// upload and the row that the route writes for it.
		now := time.Now()
		os.Chtimes(dest, now, now)
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

// room gives the bytes that an upload may take, and says if the answer is known.
// The free space is not known on every system; then the filesystem is the only
// limit, and it still gives an error when it fills up.
func (s *Store) room() (int64, bool, error) {
	free, err := fsutil.FreeBytes(s.root)
	if err != nil {
		return 0, false, nil
	}
	if free <= Reserve {
		return 0, true, fmt.Errorf("%w: the disk has %d bytes free and the reserve is %d",
			ErrNoSpace, free, Reserve)
	}
	// free is above Reserve, so the difference fits in an int64 on every disk
	// that exists.
	return int64(free - Reserve), true, nil
}

// Delete removes one object and its thumbnail. The caller checks first that no
// playlist holds the object.
func (s *Store) Delete(sha string) error {
	path, err := s.Path(sha)
	if err != nil {
		return err
	}
	thumb, err := s.ThumbPath(sha)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(thumb); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// TypeOf says what media type an object holds. The extension answers first,
// because it is what the player uses to choose an image element or a video
// element. A name with no useful extension gets the answer of
// http.DetectContentType, which reads the first bytes of the file.
func TypeOf(origName, path string) string {
	if ext := strings.ToLower(filepath.Ext(origName)); ext != "" {
		// The project table answers first. Alpine has no /etc/mime.types, so the
		// mime package alone knows almost no extension there.
		if kind := playlist.MediaType(ext); kind != "" {
			return kind
		}
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
