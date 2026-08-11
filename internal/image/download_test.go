package image

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isoServer serves a fixed body with byte-range support, mimicking the real
// mirror closely enough to exercise resume.
func isoServer(t *testing.T, body []byte, supportRanges bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if rng := r.Header.Get("Range"); rng != "" && supportRanges {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-")); err == nil {
				start = n
			}
			if start > len(body) {
				start = len(body)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[start:])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

func testRelease(url string) Release {
	return Release{Filename: "archboot-test-aarch64.iso", URL: url, Variant: variantLatest}
}

func TestFetchDownloads(t *testing.T) {
	body := bytes.Repeat([]byte("arch"), 4096)
	srv := isoServer(t, body, true)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir)
	r := testRelease(srv.URL + "/iso")

	var last Progress
	path, err := d.Fetch(context.Background(), r, func(p Progress) { last = p })
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(body))
	}
	if last.Total != int64(len(body)) {
		t.Errorf("final progress Total = %d, want %d", last.Total, len(body))
	}

	// The partial file must be gone once the download completes.
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Error("the .part file was left behind after a successful download")
	}
	if !d.Cached(r) {
		t.Error("Cached = false after a successful download")
	}
}

func TestFetchUsesCache(t *testing.T) {
	body := []byte("cached content")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write(body)
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	r := testRelease(srv.URL)

	if _, err := d.Fetch(context.Background(), r, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Fetch(context.Background(), r, nil); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("server was hit %d times; the second Fetch should have used the cache", hits)
	}
}

// An interrupted download must resume rather than start over.
func TestFetchResumes(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 1000)
	srv := isoServer(t, body, true)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir)
	r := testRelease(srv.URL)

	// Pre-seed a partial download of the first 400 bytes.
	part := filepath.Join(dir, r.Filename+".part")
	if err := os.WriteFile(part, body[:400], 0o644); err != nil {
		t.Fatal(err)
	}

	var first Progress
	seen := false
	path, err := d.Fetch(context.Background(), r, func(p Progress) {
		if !seen {
			first, seen = p, true
		}
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("resumed file is %d bytes and does not match the source (%d bytes)", len(got), len(body))
	}
	// Progress must account for the bytes already on disk, not restart at zero.
	if seen && first.Downloaded < 400 {
		t.Errorf("first progress report was %d bytes; resume should start from 400", first.Downloaded)
	}
}

// If the server ignores Range, the download must restart cleanly rather than
// appending to the stale partial file and producing a corrupt image.
func TestFetchRestartsWhenRangeIgnored(t *testing.T) {
	body := bytes.Repeat([]byte("y"), 500)
	srv := isoServer(t, body, false)
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir)
	r := testRelease(srv.URL)

	part := filepath.Join(dir, r.Filename+".part")
	if err := os.WriteFile(part, bytes.Repeat([]byte("Z"), 200), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := d.Fetch(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("file is %d bytes and contains stale data; want a clean %d-byte restart",
			len(got), len(body))
	}
}

func TestFetchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	if _, err := d.Fetch(context.Background(), testRelease(srv.URL), nil); err == nil {
		t.Error("expected an error for a 404")
	}
}

// A cancelled download must not leave a truncated file under the final name,
// where it would later be mistaken for a complete ISO.
func TestFetchCancelKeepsOnlyPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 100; i++ {
			w.Write(bytes.Repeat([]byte("a"), 1000))
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	d := NewDownloader(dir)
	r := testRelease(srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := d.Fetch(ctx, r, nil); err == nil {
		t.Fatal("expected the cancelled download to return an error")
	}
	if _, err := os.Stat(d.Path(r)); !os.IsNotExist(err) {
		t.Error("a cancelled download must not be promoted to the final filename")
	}
	if d.Cached(r) {
		t.Error("Cached reported true for a cancelled download")
	}
}

// A server that closes early while claiming a longer Content-Length must not
// yield a usable-looking ISO.
func TestFetchRejectsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.Write(bytes.Repeat([]byte("a"), 400))
	}))
	defer srv.Close()

	d := NewDownloader(t.TempDir())
	r := testRelease(srv.URL)

	if _, err := d.Fetch(context.Background(), r, nil); err == nil {
		t.Error("expected an error for a truncated transfer")
	}
	if d.Cached(r) {
		t.Error("a truncated download must not be cached as complete")
	}
}

func TestListAndRemove(t *testing.T) {
	dir := t.TempDir()
	d := NewDownloader(dir)

	if got, err := d.List(); err != nil || len(got) != 0 {
		t.Errorf("List on an empty cache = %v, %v", got, err)
	}

	r := testRelease("http://example.invalid")
	if err := os.WriteFile(d.Path(r), []byte("iso"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := d.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != r.Filename {
		t.Errorf("List = %v, want only the ISO", list)
	}

	if err := d.Remove(r); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if d.Cached(r) {
		t.Error("the ISO is still cached after Remove")
	}
	// Removing something absent is not an error.
	if err := d.Remove(r); err != nil {
		t.Errorf("Remove of a missing image should succeed, got %v", err)
	}
}

func TestProgressMath(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{"half", Progress{Downloaded: 50, Total: 100}, 0.5},
		{"unknown total", Progress{Downloaded: 50}, 0},
		{"over-reported clamps", Progress{Downloaded: 150, Total: 100}, 1},
		{"zero", Progress{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Fraction(); got != tc.want {
				t.Errorf("Fraction() = %v, want %v", got, tc.want)
			}
		})
	}

	p := Progress{Downloaded: 500, Total: 1000, BytesPerSec: 100}
	if got := p.ETA(); got != 5*time.Second {
		t.Errorf("ETA() = %v, want 5s", got)
	}
	if got := (Progress{Downloaded: 100, Total: 100, BytesPerSec: 10}).ETA(); got != 0 {
		t.Errorf("ETA() on a finished download = %v, want 0", got)
	}
	if got := (Progress{Downloaded: 1, Total: 100}).ETA(); got != 0 {
		t.Errorf("ETA() without a rate = %v, want 0", got)
	}
}

func TestSHA256(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x")
	if err := os.WriteFile(f, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Known SHA-256 of "abc".
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got, err := SHA256(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("SHA256 = %q, want %q", got, want)
	}

	if _, err := SHA256(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected an error for a missing file")
	}
}
