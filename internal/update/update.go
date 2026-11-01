// Package update reads the release feed of the Titen web app and decides
// whether the running build is behind it. The desktop app asks for it when
// it starts and offers the build for the platform it runs on.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxFeedSize bounds the release feed: it carries a handful of assets.
const maxFeedSize = 64 << 10

// Download is one asset of a release.
type Download struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Format   string `json:"format"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
}

// Release mirrors GET /version of the web app.
type Release struct {
	Tag         string     `json:"tag"`
	Version     string     `json:"version"`
	Name        string     `json:"name"`
	PublishedAt string     `json:"publishedAt"`
	ReleaseURL  string     `json:"releaseUrl"`
	Downloads   []Download `json:"downloads"`
}

// ErrNoVersion marks a feed that answered without a version to compare.
var ErrNoVersion = errors.New("release feed carries no version")

// Fetch reads the release feed of the web app at endpoint.
func Fetch(ctx context.Context, endpoint string) (*Release, error) {
	feedURL := strings.TrimSuffix(endpoint, "/") + "/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release feed answered %s", resp.Status)
	}
	var release Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxFeedSize)).Decode(&release); err != nil {
		return nil, fmt.Errorf("read release feed: %w", err)
	}
	if strings.TrimSpace(release.Version) == "" {
		return nil, ErrNoVersion
	}
	return &release, nil
}

// Newer reports whether latest is a release after current. Both may carry a
// leading v. A current that is not a dotted number, as a development build's
// "dev" is not, counts as never behind: it is not a released build.
func Newer(current, latest string) bool {
	have, ok := parseVersion(current)
	if !ok {
		return false
	}
	want, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := 0; i < max(len(have), len(want)); i++ {
		if a, b := at(have, i), at(want, i); a != b {
			return b > a
		}
	}
	return false
}

func at(nums []int, i int) int {
	if i < len(nums) {
		return nums[i]
	}
	return 0
}

// parseVersion reads a dotted number, dropping a leading v and any
// pre-release or build suffix.
func parseVersion(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	nums := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}
		nums[i] = n
	}
	return nums, true
}

// preferredFormat is the asset each platform offers first, as the download
// page does.
var preferredFormat = map[string]string{
	"macos":   "zip",
	"windows": "exe",
	"linux":   "deb",
}

// Download returns the asset of r that installs on goos/goarch, preferring
// the format the download page offers first.
func (r *Release) Download(goos, goarch string) (Download, bool) {
	platform, arch := platformOf(goos), archOf(goarch)
	if platform == "" {
		return Download{}, false
	}
	var fallback Download
	found := false
	for _, d := range r.Downloads {
		if d.Platform != platform || d.Arch != arch || d.URL == "" {
			continue
		}
		if d.Format == preferredFormat[platform] {
			return d, true
		}
		if !found {
			fallback, found = d, true
		}
	}
	return fallback, found
}

// URL is where to send the user for r: its asset for goos/goarch, else the
// release page.
func (r *Release) URL(goos, goarch string) string {
	if d, ok := r.Download(goos, goarch); ok {
		return d.URL
	}
	return r.ReleaseURL
}

// platformOf maps the runtime's GOOS to the feed's platform names.
func platformOf(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	}
	return ""
}

// archOf maps the runtime's GOARCH to the feed's arch names.
func archOf(goarch string) string {
	switch goarch {
	case "arm64":
		return "arm64"
	case "amd64", "386":
		return "x64"
	}
	return ""
}
