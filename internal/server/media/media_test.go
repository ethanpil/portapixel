package media

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutNamesTheObjectByItsHash(t *testing.T) {
	s := newStore(t)
	body := []byte("the bytes of a file")
	want := sha256.Sum256(body)

	res, err := s.Put(bytes.NewReader(body), "clip.mp4", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA256 != hex.EncodeToString(want[:]) {
		t.Fatalf("the hash is %s", res.SHA256)
	}
	if res.Size != int64(len(body)) {
		t.Fatalf("the size is %d", res.Size)
	}
	if res.Duplicate {
		t.Fatal("the first upload says duplicate")
	}
	if !s.Has(res.SHA256) {
		t.Fatal("the store does not hold the object")
	}
	// The path shards on the first two characters of the hash.
	path, err := s.Path(res.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, res.SHA256[:2]) {
		t.Fatalf("the path is %s", path)
	}
	on, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(on, body) {
		t.Fatalf("the file on the disk is %q, %v", on, err)
	}
}

func TestPutDeduplicates(t *testing.T) {
	s := newStore(t)
	body := []byte("one file, two uploads")

	first, err := s.Put(bytes.NewReader(body), "a.mp4", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(bytes.NewReader(body), "b-with-another-name.mp4", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if second.SHA256 != first.SHA256 {
		t.Fatal("the same bytes gave two hashes")
	}
	if !second.Duplicate {
		t.Fatal("the second upload of the same bytes does not say duplicate")
	}

	// No temporary file is left behind.
	entries, err := os.ReadDir(s.Root() + "/tmp")
	if err == nil && len(entries) != 0 {
		t.Fatalf("the temporary directory holds %d files", len(entries))
	}
}

func TestPutRefusesAnEmptyBody(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(bytes.NewReader(nil), "a.jpg", 0); err == nil {
		t.Fatal("an empty upload was accepted")
	}
}

// TestPutTakesABodyOfUnknownLength covers the chunked upload. Such a request gives
// a Content-Length of -1, and the old code turned that into uint64(-1). The
// free-space test then compared against 536870911 bytes and meant nothing.
func TestPutTakesABodyOfUnknownLength(t *testing.T) {
	s := newStore(t)
	body := []byte("a body whose length the request did not declare")

	res, err := s.Put(bytes.NewReader(body), "a.bin", -1)
	if err != nil {
		t.Fatalf("an upload of unknown length gave %v", err)
	}
	if res.Size != int64(len(body)) {
		t.Fatalf("the size is %d, want %d", res.Size, len(body))
	}
	if !s.Has(res.SHA256) {
		t.Fatal("the store does not hold the object")
	}
}

// TestPathRefusesAHashOfTheWrongShape keeps the check inside the store. A caller
// that forgot it must not reach a file, and a short value must never make Path read
// out of range.
func TestPathRefusesAHashOfTheWrongShape(t *testing.T) {
	s := newStore(t)
	for _, bad := range []string{"", "a", "../../server.toml", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		if _, err := s.Path(bad); !errors.Is(err, ErrBadHash) {
			t.Errorf("Path(%q) gave %v, want ErrBadHash", bad, err)
		}
		if _, err := s.ThumbPath(bad); !errors.Is(err, ErrBadHash) {
			t.Errorf("ThumbPath(%q) gave %v, want ErrBadHash", bad, err)
		}
		if s.Has(bad) {
			t.Errorf("Has(%q) says that the store holds it", bad)
		}
		if err := s.Delete(bad); !errors.Is(err, ErrBadHash) {
			t.Errorf("Delete(%q) gave %v, want ErrBadHash", bad, err)
		}
	}
}

// TestPutLeavesNoPartFileAfterAFailure covers the connection that drops halfway
// through an upload. A part file that stays behind keeps the disk full with the
// attempt that filled it.
func TestPutLeavesNoPartFileAfterAFailure(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(failingReader{}, "a.bin", -1); err == nil {
		t.Fatal("a body that fails was accepted")
	}
	entries, err := os.ReadDir(filepath.Join(s.Root(), "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the temporary directory holds %d files after a failed upload", len(entries))
	}
}

// failingReader gives one byte and then an error.
type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = 'x'
	return 1, errors.New("the connection went away")
}

// TestSweepRemovesOrphans covers the blob that no row can reach.
//
// An upload writes the blob and then the row, and a delete removes the row and then
// the file. Either failure leaves a file that no page counts and no route can
// reach. On Windows a Range download that is in flight makes the delete fail, so
// this happens on a normal day and not only after a crash.
func TestSweepRemovesOrphans(t *testing.T) {
	s := newStore(t)
	body := pngOf(t, 40, 30)
	res, err := s.Put(bytes.NewReader(body), "a.png", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}

	// A blob that a row names is never touched, whatever its age.
	known := map[string]bool{res.SHA256: true}
	out, err := s.Sweep(known, time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if out.Blobs != 0 || !s.Has(res.SHA256) {
		t.Fatalf("the sweep took an object that a row names: %+v", out)
	}

	// The row goes away. The blob is younger than the grace period, so the sweep
	// leaves it: an upload that is in flight has no row yet either.
	if out, err = s.Sweep(map[string]bool{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out.Blobs != 0 || !s.Has(res.SHA256) {
		t.Fatalf("the sweep took a young object: %+v", out)
	}

	// An hour later it goes.
	if out, err = s.Sweep(map[string]bool{}, time.Now().Add(2*orphanGrace)); err != nil {
		t.Fatal(err)
	}
	if out.Blobs != 1 {
		t.Fatalf("the sweep removed %d objects, want 1", out.Blobs)
	}
	if s.Has(res.SHA256) {
		t.Fatal("the orphan is still there")
	}
	thumb, err := s.ThumbPath(res.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(thumb); err == nil {
		t.Fatal("the thumbnail of the orphan is still there")
	}
}

// TestSweepRemovesStalePartFiles covers the upload that a crash cut short.
func TestSweepRemovesStalePartFiles(t *testing.T) {
	s := newStore(t)
	part := filepath.Join(s.Root(), "tmp", "upload12345")
	if err := os.WriteFile(part, []byte("half an upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := s.Sweep(map[string]bool{}, time.Now().Add(2*orphanGrace))
	if err != nil {
		t.Fatal(err)
	}
	if out.Temps != 1 {
		t.Fatalf("the sweep removed %d part files, want 1", out.Temps)
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal("the part file is still there")
	}
}

// pngOf makes a PNG of one colour.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 80, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestThumbnailOfAnImage(t *testing.T) {
	s := newStore(t)
	body := pngOf(t, 1200, 800)

	res, err := s.Put(bytes.NewReader(body), "welcome.png", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasThumb {
		t.Fatal("an image got no thumbnail")
	}
	if res.Width != 1200 || res.Height != 800 {
		t.Fatalf("the size is %d by %d", res.Width, res.Height)
	}

	thumb, err := s.ThumbPath(res.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(thumb)
	if err != nil {
		t.Fatalf("the thumbnail is not on the disk: %v", err)
	}
	defer f.Close()

	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("the thumbnail is not a JPEG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != ThumbWidth {
		t.Fatalf("the thumbnail is %d wide, want %d", b.Dx(), ThumbWidth)
	}
	// 1200 by 800 is 3 to 2, so 480 wide gives 320 high.
	if b.Dy() != 320 {
		t.Fatalf("the thumbnail is %d high, want 320", b.Dy())
	}
}

func TestSmallImageKeepsItsSize(t *testing.T) {
	s := newStore(t)
	body := pngOf(t, 100, 50)

	res, err := s.Put(bytes.NewReader(body), "small.png", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasThumb {
		t.Fatal("a small image got no thumbnail")
	}
	thumb, err := s.ThumbPath(res.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(thumb)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 100 {
		t.Fatalf("a small image was scaled to %d", img.Bounds().Dx())
	}
}

func TestDecompressionBombIsRefused(t *testing.T) {
	// A PNG header that declares 40000 by 40000 pixels is 1.6 thousand million
	// pixels. A decode of it would ask for about 6 GB. The guard must refuse it
	// from the header, so this test needs no such file: it makes the header only.
	s := newStore(t)
	body := bombPNG(t, 40000, 40000)

	res, err := s.Put(bytes.NewReader(body), "bomb.png", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasThumb {
		t.Fatal("the bomb got a thumbnail, so the guard did not hold")
	}
	if res.Width != 40000 || res.Height != 40000 {
		t.Fatalf("the header says %d by %d", res.Width, res.Height)
	}
	if thumb, pathErr := s.ThumbPath(res.SHA256); pathErr != nil {
		t.Fatal(pathErr)
	} else if _, err := os.Stat(thumb); err == nil {
		t.Fatal("a thumbnail file is on the disk")
	}
	// The object itself is kept: it is the user's file, and only the thumbnail
	// step gave up.
	if !s.Has(res.SHA256) {
		t.Fatal("the store did not keep the object")
	}
}

// bombPNG makes a PNG whose header declares a very large picture. The image data
// is the data of a small picture, so the file stays small and only the header
// lies. That is what a decompression bomb is.
func bombPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	data := pngOf(t, 4, 4)
	// The IHDR chunk: length at byte 8, the type "IHDR" at 12, the width at 16,
	// the height at 20, and the chunk checksum at 29. The PNG reader checks that
	// checksum, so it has to be made again over the type and the data.
	put32(data, 16, uint32(w))
	put32(data, 20, uint32(h))
	put32(data, 29, crc32.ChecksumIEEE(data[12:29]))
	return data
}

func put32(b []byte, off int, v uint32) {
	b[off] = byte(v >> 24)
	b[off+1] = byte(v >> 16)
	b[off+2] = byte(v >> 8)
	b[off+3] = byte(v)
}

func TestFileThatIsNotAnImageGetsNoThumbnail(t *testing.T) {
	s := newStore(t)
	body := []byte("this is not a picture, it is a line of text")

	res, err := s.Put(bytes.NewReader(body), "clip.mp4", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasThumb {
		t.Fatal("a file that is not an image got a thumbnail")
	}
}

func TestTypeOfUsesTheExtensionFirst(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/x"
	if err := os.WriteFile(path, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := TypeOf("a.mp4", path); !strings.Contains(got, "mp4") {
		t.Fatalf("the type of a.mp4 is %q", got)
	}
	// With no useful extension the first bytes answer.
	if got := TypeOf("noextension", path); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("the type of a text file is %q", got)
	}
}

func TestDeleteRemovesTheObjectAndTheThumbnail(t *testing.T) {
	s := newStore(t)
	body := pngOf(t, 600, 400)
	res, err := s.Put(bytes.NewReader(body), "a.png", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(res.SHA256); err != nil {
		t.Fatal(err)
	}
	if s.Has(res.SHA256) {
		t.Fatal("the object is still there")
	}
	if thumb, pathErr := s.ThumbPath(res.SHA256); pathErr != nil {
		t.Fatal(pathErr)
	} else if _, err := os.Stat(thumb); err == nil {
		t.Fatal("the thumbnail is still there")
	}
	// A second delete is not a fault: the caller may retry.
	if err := s.Delete(res.SHA256); err != nil {
		t.Fatalf("the second delete gave %v", err)
	}
}

func TestScaleKeepsTheMeanColour(t *testing.T) {
	// A box filter over one colour must give that colour back. A scaler that
	// dropped pixels would pass this too, so the test above checks the size and
	// this one checks that the mean is not a rounding mistake.
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 1000; x++ {
			src.Set(x, y, color.RGBA{R: 40, G: 160, B: 90, A: 255})
		}
	}
	out := scale(src, 100)
	if out.Bounds().Dx() != 100 || out.Bounds().Dy() != 50 {
		t.Fatalf("the result is %v", out.Bounds())
	}
	r, g, b, a := out.At(50, 25).RGBA()
	if r>>8 != 40 || g>>8 != 160 || b>>8 != 90 || a>>8 != 255 {
		t.Fatalf("the colour is %d %d %d %d", r>>8, g>>8, b>>8, a>>8)
	}
}
