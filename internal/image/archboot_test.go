package image

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// realIndexHTML mirrors the shape of Archboot's actual directory listing,
// including the sibling .sig files and the size column.
const realIndexHTML = `<html><head><title>Index of /aarch64/latest/iso/</title></head><body>
<h1>Index of /aarch64/latest/iso/</h1>
<table>
<tr><td><a href="../">Parent Directory</a></td><td>&nbsp;</td><td align="right">  - </td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso">archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso</a></td><td align="right">470410 KB</td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso.sig">...sig</a></td><td align="right">1 KB</td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-latest-aarch64.iso">archboot-...-latest-aarch64.iso</a></td><td align="right">296668 KB</td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-latest-aarch64.iso.sig">...sig</a></td><td align="right">1 KB</td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-local-aarch64.iso">archboot-...-local-aarch64.iso</a></td><td align="right">1002614 KB</td></tr>
<tr><td><a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-local-aarch64.iso.sig">...sig</a></td><td align="right">1 KB</td></tr>
</table></body></html>`

type stubFetcher struct {
	body string
	err  error
	got  string
}

func (f *stubFetcher) Get(_ context.Context, url string) ([]byte, error) {
	f.got = url
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.body), nil
}

func TestParseIndex(t *testing.T) {
	releases := ParseIndex(realIndexHTML)

	if len(releases) != 3 {
		t.Fatalf("got %d releases, want 3 (one per variant); got %+v", len(releases), releases)
	}

	byVariant := map[variant]Release{}
	for _, r := range releases {
		byVariant[r.Variant] = r
	}

	for _, want := range []variant{variantStandard, variantLatest, variantLocal} {
		r, ok := byVariant[want]
		if !ok {
			t.Errorf("variant %q was not parsed out of the index", want)
			continue
		}
		if r.Date != "2026.08.11" {
			t.Errorf("%s: Date = %q, want 2026.08.11", want, r.Date)
		}
		if r.Version != "7.1.8-1" {
			t.Errorf("%s: Version = %q, want 7.1.8-1", want, r.Version)
		}
		if !strings.HasPrefix(r.URL, IndexURL) || !strings.HasSuffix(r.URL, ".iso") {
			t.Errorf("%s: URL = %q looks wrong", want, r.URL)
		}
	}

	// The variantless filename is the standard build; it must not be mistaken
	// for one of the suffixed variants.
	std := byVariant[variantStandard]
	if strings.Contains(std.Filename, "-latest-") || strings.Contains(std.Filename, "-local-") {
		t.Errorf("standard variant resolved to %q", std.Filename)
	}
}

// Signature files sit next to every ISO and must never be offered as media.
func TestParseIndexIgnoresSignatures(t *testing.T) {
	for _, r := range ParseIndex(realIndexHTML) {
		if strings.HasSuffix(r.Filename, ".sig") {
			t.Errorf("a .sig file was parsed as a release: %q", r.Filename)
		}
	}
}

func TestParseIndexSizeHint(t *testing.T) {
	byVariant := map[variant]Release{}
	for _, r := range ParseIndex(realIndexHTML) {
		byVariant[r.Variant] = r
	}

	// 470410 KB, as listed.
	if got := byVariant[variantStandard].SizeHint; got != 470410*1024 {
		t.Errorf("standard SizeHint = %d, want %d", got, 470410*1024)
	}
	if got := byVariant[variantLocal].SizeHint; got != 1002614*1024 {
		t.Errorf("local SizeHint = %d, want %d", got, 1002614*1024)
	}
}

func TestParseIndexEmptyAndJunk(t *testing.T) {
	for _, in := range []string{"", "<html></html>", "not html at all", `<a href="readme.txt">x</a>`} {
		if got := ParseIndex(in); len(got) != 0 {
			t.Errorf("ParseIndex(%q) = %v, want nothing", in, got)
		}
	}
}

// When several builds are listed the newest must sort first.
func TestParseIndexPrefersNewest(t *testing.T) {
	html := `
<a href="archboot-2026.01.02-01.00-7.0.0-1-aarch64-ARCH-aarch64.iso">old</a>
<a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso">new</a>`

	releases := ParseIndex(html)
	if len(releases) != 2 {
		t.Fatalf("got %d releases, want 2", len(releases))
	}
	if releases[0].Date != "2026.08.11" {
		t.Errorf("first release is %q, want the newest build first", releases[0].Date)
	}
}

// march installs from the "latest" build and offers no choice, so Resolve must
// return that one specifically rather than whatever sorts first.
func TestResolvePicksLatestBuild(t *testing.T) {
	f := &stubFetcher{body: realIndexHTML}

	r, err := Resolve(context.Background(), f)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Variant != variantLatest {
		t.Errorf("Variant = %q, want the latest build", r.Variant)
	}
	if !strings.Contains(r.Filename, "-latest-") {
		t.Errorf("Filename = %q, want the latest build", r.Filename)
	}
	if f.got != IndexURL {
		t.Errorf("fetched %q, want %q", f.got, IndexURL)
	}
}

func TestResolveErrors(t *testing.T) {
	t.Run("network failure", func(t *testing.T) {
		f := &stubFetcher{err: errors.New("dial tcp: no route to host")}
		if _, err := Resolve(context.Background(), f); err == nil {
			t.Error("expected the fetch error to surface")
		}
	})

	t.Run("empty index", func(t *testing.T) {
		f := &stubFetcher{body: "<html></html>"}
		_, err := Resolve(context.Background(), f)
		if err == nil {
			t.Fatal("expected an error for an index with no ISOs")
		}
		if !strings.Contains(err.Error(), "no aarch64 ISOs") {
			t.Errorf("error %q should explain that nothing was found", err)
		}
	})

	t.Run("index without the latest build", func(t *testing.T) {
		html := `<a href="archboot-2026.08.11-02.30-7.1.8-1-aarch64-ARCH-aarch64.iso">x</a>`
		f := &stubFetcher{body: html}
		if _, err := Resolve(context.Background(), f); err == nil {
			t.Error("expected an error when the latest build is absent")
		}
	})
}

func TestResolveAll(t *testing.T) {
	f := &stubFetcher{body: realIndexHTML}
	all, err := ResolveAll(context.Background(), f)
	if err != nil {
		t.Fatalf("ResolveAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("got %d releases, want 3", len(all))
	}
}

// TestResolveLive checks the parser against the real Archboot index. Archboot
// rebuilds frequently, so a change to their filename scheme would silently
// break installs; this catches that.
func TestResolveLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping a test that hits the network")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	releases, err := ResolveAll(ctx, nil)
	if err != nil {
		t.Skipf("cannot reach %s: %v", IndexURL, err)
	}

	if len(releases) == 0 {
		t.Fatal("the live Archboot index yielded no aarch64 ISOs; the filename scheme may have changed")
	}

	seen := map[variant]bool{}
	for _, r := range releases {
		seen[r.Variant] = true
		if r.Date == "" || r.Version == "" {
			t.Errorf("release %q did not parse cleanly: %+v", r.Filename, r)
		}
		if !strings.HasSuffix(r.URL, ".iso") {
			t.Errorf("release URL %q is not an ISO", r.URL)
		}
	}
	// march installs from the latest build, so it must exist upstream.
	if !seen[variantLatest] {
		t.Errorf("the latest build is missing from the live index; got %v", seen)
	}

	t.Logf("live index: %d ISOs, newest %s (archboot %s)",
		len(releases), releases[0].Date, releases[0].Version)
}
