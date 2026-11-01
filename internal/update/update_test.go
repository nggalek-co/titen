package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestNewerComparesDottedVersions(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		newer           bool
	}{
		{"0.0.8", "0.0.9", true},
		{"0.0.8", "v0.0.9", true},
		{"v0.0.8", "0.0.9", true},
		{"0.0.9", "0.0.8", false},
		{"0.0.8", "0.0.8", false},
		{"0.1", "0.1.0", false},
		{"0.1", "0.2", true},
		{"1.0.0", "1.0.0-1", false},
		{"0.0.8-1", "0.0.8", false},
		{"dev", "0.0.9", false},
		{"0.0.8", "dev", false},
		{"0.0.8", "", false},
	} {
		if got := Newer(tc.current, tc.latest); got != tc.newer {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.newer)
		}
	}
}

func TestDownloadPicksThePlatformBuild(t *testing.T) {
	release := &Release{
		ReleaseURL: "https://example.test/releases/v0.0.9",
		Downloads: []Download{
			{Platform: "macos", Arch: "arm64", Format: "dmg", URL: "https://example.test/titen.dmg"},
			{Platform: "macos", Arch: "arm64", Format: "zip", URL: "https://example.test/titen.zip"},
			{Platform: "macos", Arch: "x64", Format: "zip", URL: "https://example.test/titen-x64.zip"},
			{Platform: "linux", Arch: "x64", Format: "deb", URL: "https://example.test/titen.deb"},
		},
	}
	got, ok := release.Download("darwin", "arm64")
	if !ok || got.URL != "https://example.test/titen.zip" {
		t.Fatalf("macos arm64 = %+v, %v", got, ok)
	}
	if got, ok = release.Download("linux", "amd64"); !ok || got.URL != "https://example.test/titen.deb" {
		t.Fatalf("linux amd64 = %+v, %v", got, ok)
	}
	if _, ok = release.Download("windows", "amd64"); ok {
		t.Fatal("windows matched a build it has none of")
	}
	if url := release.URL("windows", "amd64"); url != release.ReleaseURL {
		t.Fatalf("windows URL = %q, want the release page", url)
	}
	if url := release.URL("darwin", "arm64"); url != "https://example.test/titen.zip" {
		t.Fatalf("macos URL = %q", url)
	}
}

func TestDownloadFallsBackToAnyFormat(t *testing.T) {
	release := &Release{Downloads: []Download{{Platform: "linux", Arch: "arm64", Format: "appimage", URL: "https://example.test/titen.appimage"}}}
	got, ok := release.Download("linux", "arm64")
	if !ok || got.Format != "appimage" {
		t.Fatalf("fallback = %+v, %v", got, ok)
	}
	if _, ok = release.Download("plan9", "arm64"); ok {
		t.Fatal("an unknown platform matched")
	}
}

// testdata/version.json is the payload the web app served on 2026-11-01. The
// /version route and /api/desktop-releases share it.
func TestDownloadPicksFromTheServedFeed(t *testing.T) {
	data, err := os.ReadFile("testdata/version.json")
	if err != nil {
		t.Fatal(err)
	}
	var release Release
	if err = json.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.0.8" || len(release.Downloads) == 0 {
		t.Fatalf("release = %+v", release)
	}
	for _, tc := range []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "macos/arm64/zip"},
		{"darwin", "amd64", "macos/x64/zip"},
		{"windows", "amd64", "windows/x64/exe"},
		{"linux", "amd64", "linux/x64/deb"},
	} {
		d, ok := release.Download(tc.goos, tc.goarch)
		if got := d.Platform + "/" + d.Arch + "/" + d.Format; !ok || got != tc.want {
			t.Errorf("%s/%s = %q, %v; want %q", tc.goos, tc.goarch, got, ok, tc.want)
		}
	}
}

func TestFetchReadsTheVersionRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag":"v0.0.9","version":"0.0.9","name":"Titen 0.0.9","publishedAt":"2026-10-31T00:00:00Z","releaseUrl":"https://example.test/releases/v0.0.9","downloads":[{"platform":"macos","arch":"arm64","format":"zip","url":"https://example.test/titen.zip","size":42}]}`))
	}))
	defer server.Close()

	release, err := Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.Version != "0.0.9" || release.Name != "Titen 0.0.9" || len(release.Downloads) != 1 {
		t.Fatalf("release = %+v", release)
	}
	if got, ok := release.Download("darwin", "arm64"); !ok || got.Size != 42 {
		t.Fatalf("download = %+v, %v", got, ok)
	}
}

func TestFetchRejectsBadFeeds(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer broken.Close()
	if _, err := Fetch(context.Background(), broken.URL); err == nil {
		t.Fatal("a failing feed was read")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":""}`))
	}))
	defer empty.Close()
	if _, err := Fetch(context.Background(), empty.URL); !errors.Is(err, ErrNoVersion) {
		t.Fatalf("empty version = %v, want ErrNoVersion", err)
	}
}
