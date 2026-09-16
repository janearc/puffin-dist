package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// The maps browser: browse the files rather than display them, and assess
// whether what is listed is actually there on the other end, how big it is
// and what else is true of it.
//
// kingfisher already serves everything this needs: /stats carries the mount
// table, GET on a directory returns a JSON index with names and byte sizes, and
// HEAD on a file answers with the truth -- status, length, type, cache posture
// -- without moving the bytes.
//
// Browsing is walking those; assessing is one HEAD, made deliberately on
// request rather than in bulk, because hammering every file with HEADs to build
// a listing is a scan, not a browse.

// MapEntry is one row of a directory listing.
type MapEntry struct {
	Name  string
	Dir   bool
	Bytes int64 // from the listing; -1 for directories
}

// MapListing is one directory as kingfisher describes it.
type MapListing struct {
	Mount    string
	Path     string // the browsed path, mount-relative
	Manifest string
	Entries  []MapEntry
}

// MapAttrs is what HEAD said about one file: the availability assessment.
type MapAttrs struct {
	Status       int
	Bytes        int64
	ContentType  string
	LastModified string
	CacheControl string
	Ranges       bool
}

// Available is the assessment in one word: the file the listing promised is
// actually there and whole.
func (a MapAttrs) Available() bool { return a.Status == 200 }

// fetchMounts reads the mount table from kingfisher's stats.
func fetchMounts(base string) ([]string, error) {
	resp, err := client.Get(base + "/stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Mounts map[string]string `json:"mounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf(
			"stats is not kingfisher-shaped: %w",
			err,
		)
	}
	mounts := make([]string, 0, len(out.Mounts))
	for m := range out.Mounts {
		mounts = append(mounts, m)
	}
	sort.Strings(mounts)
	return mounts, nil
}

// fetchListing reads one directory's index.
func fetchListing(base, path string) (*MapListing, error) {
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	resp, err := client.Get(base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Mount       string   `json:"mount"`
		Manifest    string   `json:"manifest"`
		Directories []string `json:"directories"`
		Files       []struct {
			Name  string `json:"name"`
			Bytes int64  `json:"bytes"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf(
			"%s did not answer with a listing: %w",
			path,
			err,
		)
	}
	l := &MapListing{Mount: out.Mount, Path: path, Manifest: out.Manifest}
	for _, d := range out.Directories {
		l.Entries = append(
			l.Entries,
			MapEntry{Name: d, Dir: true, Bytes: -1},
		)
	}
	for _, f := range out.Files {
		l.Entries = append(
			l.Entries,
			MapEntry{Name: f.Name, Bytes: f.Bytes},
		)
	}
	return l, nil
}

// headFile performs the availability assessment for one file.
func headFile(base, path string) (MapAttrs, error) {
	req, err := http.NewRequest(http.MethodHead, base+path, nil)
	if err != nil {
		return MapAttrs{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return MapAttrs{}, err
	}
	resp.Body.Close()
	var n int64 = -1
	fmt.Sscanf(resp.Header.Get("Content-Length"), "%d", &n)
	return MapAttrs{
		Status:       resp.StatusCode,
		Bytes:        n,
		ContentType:  resp.Header.Get("Content-Type"),
		LastModified: resp.Header.Get("Last-Modified"),
		CacheControl: resp.Header.Get("Cache-Control"),
		Ranges:       resp.Header.Get("Accept-Ranges") == "bytes",
	}, nil
}

// humanBytes renders sizes the way a person reads them.
func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "-"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1024*1024*1024))
	}
}
