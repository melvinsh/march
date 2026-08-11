// Package image discovers and downloads Arch Linux installer media for
// aarch64.
//
// Arch Linux publishes no aarch64 ISO of its own, and the Arch Linux ARM
// project ships only rootfs tarballs that must be unpacked onto an ext4
// filesystem as root — impossible to do natively on macOS. Archboot fills the
// gap: it builds bootable UEFI aarch64 ISOs carrying a full Arch installer,
// which is the one path that works identically on every host march runs on.
package image

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// IndexURL is Archboot's directory listing for the current aarch64 release.
const IndexURL = "https://release.archboot.com/aarch64/latest/iso/"

// variant distinguishes the three builds Archboot publishes. march always
// installs from the "latest" build: it is the smallest download and carries
// current packages, and since installing a desktop needs the network anyway,
// the larger offline-capable builds buy nothing.
type variant string

const (
	variantStandard variant = "standard"
	variantLatest   variant = "latest"
	variantLocal    variant = "local"
)

// Release is one downloadable Archboot ISO.
type Release struct {
	Filename string
	URL      string
	Variant  variant
	Version  string // Archboot build version, e.g. "7.1.8-1"
	Date     string // build date, e.g. "2026.08.11"
	SizeHint int64  // bytes, from the index listing; 0 when unknown
}

// isoRe matches Archboot's aarch64 filenames, e.g.
// archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-latest-aarch64.iso
// The middle segment before the trailing "-aarch64.iso" carries the variant.
var isoRe = regexp.MustCompile(
	`archboot-(\d{4}\.\d{2}\.\d{2})-[\d.]+-([\d.]+-\d+)-aarch64-ARCH-(?:(latest|local)-)?aarch64\.iso`)

// hrefRe pulls candidate filenames out of the HTML directory index.
var hrefRe = regexp.MustCompile(`href="([^"]+\.iso)"`)

// sizeRe finds a "12345" or "1,234 KB"-style size following a link, used only
// as a display hint.
var sizeRe = regexp.MustCompile(`([\d,]+)\s*(?:KB|K)\b`)

// Fetcher retrieves the Archboot release index. It is an interface so the
// resolver can be tested without network access.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// HTTPFetcher is the production Fetcher.
type HTTPFetcher struct{ Client *http.Client }

// Get performs a plain GET and returns the body.
func (f HTTPFetcher) Get(ctx context.Context, url string) ([]byte, error) {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// The index is a small HTML page; cap the read so a redirect to something
	// enormous cannot exhaust memory.
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

const userAgent = "march/1.0 (+https://github.com/melvinsh/march)"

// ParseIndex extracts every aarch64 ISO from an Archboot directory listing.
func ParseIndex(html string) []Release {
	seen := map[string]bool{}
	var out []Release

	for _, m := range hrefRe.FindAllStringSubmatchIndex(html, -1) {
		name := html[m[2]:m[3]]
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if seen[name] {
			continue
		}
		fields := isoRe.FindStringSubmatch(name)
		if fields == nil {
			continue
		}
		seen[name] = true

		v := variantStandard
		if fields[3] != "" {
			v = variant(fields[3])
		}
		out = append(out, Release{
			Filename: name,
			URL:      IndexURL + name,
			Variant:  v,
			Date:     fields[1],
			Version:  fields[2],
			SizeHint: sizeAfter(html, m[1]),
		})
	}

	// Newest first, so callers can take the head of the list.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].Filename < out[j].Filename
	})
	return out
}

// sizeAfter reads the size column that follows a link in the index listing.
// It is a display nicety, so any failure simply yields zero.
func sizeAfter(html string, from int) int64 {
	end := from + 200
	if end > len(html) {
		end = len(html)
	}
	m := sizeRe.FindStringSubmatch(html[from:end])
	if m == nil {
		return 0
	}
	var n int64
	for _, r := range m[1] {
		if r >= '0' && r <= '9' {
			n = n*10 + int64(r-'0')
		}
	}
	return n * 1024
}

// Resolve fetches the release index and returns the ISO march installs from.
func Resolve(ctx context.Context, f Fetcher) (Release, error) {
	releases, err := ResolveAll(ctx, f)
	if err != nil {
		return Release{}, err
	}
	for _, r := range releases {
		if r.Variant == variantLatest {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("the current Archboot release has no %q build", variantLatest)
}

// ResolveAll fetches the index and returns every ISO in the current release.
func ResolveAll(ctx context.Context, f Fetcher) ([]Release, error) {
	if f == nil {
		f = HTTPFetcher{}
	}
	body, err := f.Get(ctx, IndexURL)
	if err != nil {
		return nil, fmt.Errorf("fetching the Archboot release index: %w", err)
	}
	releases := ParseIndex(string(body))
	if len(releases) == 0 {
		return nil, fmt.Errorf("no aarch64 ISOs found in the Archboot index at %s", IndexURL)
	}
	return releases, nil
}
