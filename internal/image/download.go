package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// Progress reports download state. Total is zero when the server does not
// declare a length.
type Progress struct {
	Downloaded  int64
	Total       int64
	BytesPerSec float64
}

// Fraction returns completion in [0,1], or 0 when the total is unknown.
func (p Progress) Fraction() float64 {
	if p.Total <= 0 {
		return 0
	}
	f := float64(p.Downloaded) / float64(p.Total)
	if f > 1 {
		return 1
	}
	return f
}

// ETA estimates the remaining time, or zero when it cannot be known.
func (p Progress) ETA() time.Duration {
	if p.Total <= 0 || p.BytesPerSec <= 0 || p.Downloaded >= p.Total {
		return 0
	}
	return time.Duration(float64(p.Total-p.Downloaded)/p.BytesPerSec) * time.Second
}

// Downloader fetches installer images into a local cache.
type Downloader struct {
	Dir    string
	Client *http.Client
}

// NewDownloader returns a Downloader caching into dir.
func NewDownloader(dir string) *Downloader {
	return &Downloader{
		Dir: dir,
		Client: &http.Client{
			// No overall timeout: these are multi-hundred-megabyte transfers.
			// Stalls are caught by the per-read deadline in the response body.
			Timeout: 0,
		},
	}
}

// Path is where a release is cached once complete.
func (d *Downloader) Path(r Release) string { return filepath.Join(d.Dir, r.Filename) }

// partPath is the in-progress file. Keeping it distinct from the final name
// means an interrupted download is never mistaken for a usable ISO.
func (d *Downloader) partPath(r Release) string { return d.Path(r) + ".part" }

// Cached reports whether a release is already fully downloaded.
func (d *Downloader) Cached(r Release) bool {
	fi, err := os.Stat(d.Path(r))
	return err == nil && fi.Size() > 0
}

// Fetch downloads a release, resuming a previous partial transfer when the
// server supports range requests. It reports progress through onProgress,
// which may be nil. The returned path is the cached ISO.
func (d *Downloader) Fetch(ctx context.Context, r Release, onProgress func(Progress)) (string, error) {
	final := d.Path(r)
	if d.Cached(r) {
		return final, nil
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", fmt.Errorf("creating the image cache: %w", err)
	}

	part := d.partPath(r)
	var resumeFrom int64
	if fi, err := os.Stat(part); err == nil {
		resumeFrom = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}

	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", r.Filename, err)
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		// The server ignored the range request, so start over.
		resumeFrom = 0
		flags |= os.O_TRUNC
	default:
		return "", fmt.Errorf("downloading %s: %s", r.Filename, resp.Status)
	}

	total := resp.ContentLength
	if total > 0 {
		total += resumeFrom
	} else if r.SizeHint > 0 {
		total = r.SizeHint
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return "", fmt.Errorf("opening the partial download: %w", err)
	}

	counter := &progressWriter{
		downloaded: resumeFrom,
		total:      total,
		onProgress: onProgress,
		started:    time.Now(),
		startBytes: resumeFrom,
	}

	_, copyErr := io.Copy(io.MultiWriter(f, counter), resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		// The partial file is deliberately kept so the next attempt resumes.
		return "", fmt.Errorf("downloading %s: %w", r.Filename, copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("finishing %s: %w", r.Filename, closeErr)
	}

	// A truncated transfer that reported no error still must not be promoted
	// to a usable ISO.
	if total > 0 {
		fi, err := os.Stat(part)
		if err != nil {
			return "", err
		}
		if fi.Size() != total {
			return "", fmt.Errorf("download of %s is incomplete: got %d of %d bytes",
				r.Filename, fi.Size(), total)
		}
	}

	if err := os.Rename(part, final); err != nil {
		return "", fmt.Errorf("installing %s into the cache: %w", r.Filename, err)
	}
	if onProgress != nil {
		onProgress(Progress{Downloaded: total, Total: total})
	}
	return final, nil
}

// Remove deletes a cached image and any partial download for it.
func (d *Downloader) Remove(r Release) error {
	var errs []error
	for _, p := range []string{d.Path(r), d.partPath(r)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// List returns every cached ISO filename.
func (d *Downloader) List() ([]string, error) {
	entries, err := os.ReadDir(d.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".iso" {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// progressWriter counts bytes and throttles progress callbacks, so a fast
// transfer cannot flood the UI with messages.
type progressWriter struct {
	downloaded int64
	total      int64
	startBytes int64
	started    time.Time
	lastReport atomic.Int64
	onProgress func(Progress)
}

const reportInterval = 100 * time.Millisecond

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.downloaded += int64(n)
	if w.onProgress == nil {
		return n, nil
	}

	now := time.Now()
	last := w.lastReport.Load()
	if last != 0 && now.Sub(time.Unix(0, last)) < reportInterval {
		return n, nil
	}
	w.lastReport.Store(now.UnixNano())

	var rate float64
	if elapsed := now.Sub(w.started).Seconds(); elapsed > 0 {
		rate = float64(w.downloaded-w.startBytes) / elapsed
	}
	w.onProgress(Progress{Downloaded: w.downloaded, Total: w.total, BytesPerSec: rate})
	return n, nil
}

// SHA256 computes a file's checksum, used to verify an image the user supplies
// out of band.
func SHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
