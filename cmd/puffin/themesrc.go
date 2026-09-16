package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Themes come from dodo, live.
//
// One presentation, everywhere it is presented.
//
// A palette transcribed into this file is a copy that starts drifting the
// moment it is written, and it already had:
//
// the four themes baked below were transcribed from dodo's
// lib/themes/themes.css, while the deployed dodo serves a newer token
// vocabulary (--ground/--raised/--rule, with --panel marked deprecated in its
// own comments).
//
// Both were true at once, which is what a copy buys you. So puffin ASKS.
//
// Two files, in dodo's own load order:
//
//   /look/themes/midnight.css   the complete token base -- corvid IS
//                               midnight, "one palette in two spellings"
//   /look/themes/dynamic.css    per-theme colour overrides, keyed off
//                               data-theme, one block per theme
//
// Themes are the one place last-known-good caching is legal here, which
// DESIGN.md says out loud: a stale colour misleads nobody, a stale flag
// does.
//
// So the order is network, then cache, then the baked fallback -- and which one
// answered is stated on the roster rather than hidden, because a tool that
// silently serves you yesterday's answer is how you stop trusting screens.
//
// What is NOT pulled: --ok, --warn and --caution. dodo's own comment says
// they are never themed, for the reason its css states -- a warning that
// changes colour with the theme is not a warning. They stay baked here, and
// pulling them would be adopting a bug.

// dodoThemePaths are the two files, in load order.
var dodoThemePaths = []string{
	"/look/themes/midnight.css",
	"/look/themes/dynamic.css",
}

// themesFetched carries a pull back into the loop.
type themesFetched struct {
	themes []Theme
	source string // "dodo" | "cache" | ""
	err    error
}

// themesCmd pulls the themes without blocking the splash.
func themesCmd(domain string) tea.Cmd {
	base := fmt.Sprintf("http://dodo.%s", domain)
	return func() tea.Msg {
		themes, src, err := loadThemes(base)
		return themesFetched{themes: themes, source: src, err: err}
	}
}

// loadThemes tries dodo, then the cache. A failure of both is not an error
// the caller has to handle -- the baked themes are always there -- but it IS
// reported, so the roster can say what puffin is wearing and why.
func loadThemes(base string) ([]Theme, string, error) {
	css, err := fetchThemeCSS(base)
	if err == nil {
		if themes := parseThemes(css); len(themes) > 0 {
			writeThemeCache(
				css,
				// best effort: a cache that fails to write is
				// not an outage
			)
			return themes, "dodo", nil
		}
		err = fmt.Errorf("dodo served no themes puffin could read")
	}
	if cached, cerr := readThemeCache(); cerr == nil {
		if themes := parseThemes(cached); len(themes) > 0 {
			return themes, "cache", err
		}
	}
	return nil, "", err
}

// fetchThemeCSS gets both files and concatenates them in dodo's load order.
func fetchThemeCSS(base string) ([]string, error) {
	out := make([]string, 0, len(dodoThemePaths))
	for _, p := range dodoThemePaths {
		resp, err := client.Get(base + p)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("%s: %d", p, resp.StatusCode)
		}
		// dodo's front door answers every path with its index page, so
		// a 200 proves nothing. The same lesson the roster learned from
		// traefik: check what the wire actually said, not what it
		// claimed.
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(
			ct,
			"css",
		) {
			return nil, fmt.Errorf(
				"%s answered %s, not css -- that is the spa "+
					"fallback",
				p,
				ct,
			)
		}
		out = append(out, string(body))
	}
	return out, nil
}

var (
	blockRe = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	varRe   = regexp.MustCompile(`--([a-z0-9-]+)\s*:\s*([^;]+);`)
	themeRe = regexp.MustCompile(`\[data-theme="([a-z0-9-]+)"\]`)
)

// parseThemes reads the token blocks. The base file's :root is corvid; each
// data-theme block in the overrides file is one more theme, layered on it.
func parseThemes(css []string) []Theme {
	base := map[string]string{}
	over := map[string]map[string]string{}
	for i, doc := range css {
		for _, m := range blockRe.FindAllStringSubmatch(doc, -1) {
			sel, body := strings.TrimSpace(m[1]), m[2]
			vars := map[string]string{}
			for _, v := range varRe.FindAllStringSubmatch(
				body,
				-1,
			) {
				vars["--"+v[1]] = strings.TrimSpace(v[2])
			}
			if len(vars) == 0 {
				continue
			}
			switch {
			case i == 0 && strings.Contains(sel, ":root"):
				for k, v := range vars {
					base[k] = v
				}
			case themeRe.MatchString(sel):
				name := themeRe.FindStringSubmatch(sel)[1]
				if over[name] == nil {
					over[name] = map[string]string{}
				}
				for k, v := range vars {
					over[name][k] = v
				}
			}
		}
	}
	if len(base) == 0 {
		return nil
	}
	// corvid needs no block: it IS the base, one palette in two spellings
	themes := []Theme{themeFrom("corvid", base, nil)}
	names := make([]string, 0, len(over))
	for n := range over {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		themes = append(themes, themeFrom(n, base, over[n]))
	}
	// the palette puffin has always worn stays on the end of the cycle: it
	// is puffin's own, and not dodo's to serve
	return append(themes, DodoDark())
}

// themeFrom maps dodo's tokens onto puffin's. The names are dodo's current
// ones; the older --bg/--panel spelling is accepted as a fallback so a
// half-migrated dodo still themes the terminal rather than half-theming it.
func themeFrom(name string, base, over map[string]string) Theme {
	// the RAW tokens are kept, not just the ones puffin paints with.
	// puffin-auklet inks its stencil from dodo's roles -- Dark, Light,
	// Wing, BeakBase, BeakBand, BeakTip, Feet -- which is more of the
	// palette than a terminal needs.
	//
	// Discarding what puffin does not use would force the bird to fetch the
	// same stylesheet a second time, and then the bird and the terminal
	// could disagree about what theme they are wearing.
	tokens := map[string]string{}
	for k, v := range base {
		tokens[k] = v
	}
	for k, v := range over {
		tokens[k] = v
	}
	get := func(keys ...string) lipgloss.Color {
		for _, k := range keys {
			if v, ok := over[k]; ok && isHex(v) {
				return lipgloss.Color(v)
			}
		}
		for _, k := range keys {
			if v, ok := base[k]; ok && isHex(v) {
				return lipgloss.Color(v)
			}
		}
		return ""
	}
	t := Theme{
		Name:        name,
		Bg:          get("--ground", "--bg"),
		Panel:       get("--raised", "--surface-1", "--panel"),
		Raised:      get("--raised-2", "--surface-2", "--raised"),
		Line:        get("--rule", "--line"),
		Ink:         get("--ink"),
		Dim:         get("--dim"),
		Accent:      get("--accent", "--amber"),
		AccentToken: get("--cyan", "--accent-token"),
	}
	// light is measured, not listed: a theme dodo adds tomorrow gets the
	// light-ground handling without puffin being taught its name
	t.Light = luminance(string(t.Bg)) > 0.5
	t.Tokens = tokens
	return semantics(t)
}

// isHex keeps gradients, var() references and colour functions out: puffin
// paints terminal cells and can only spend a flat colour.
func isHex(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) != 4 && len(v) != 7 || !strings.HasPrefix(v, "#") {
		return false
	}
	_, err := strconv.ParseUint(v[1:], 16, 32)
	return err == nil
}

// luminance is the perceptual brightness of a hex colour, 0 dark to 1 light.
func luminance(hex string) float64 {
	if !isHex(hex) {
		return 0
	}
	h := hex[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0
	}
	r := float64((n>>16)&0xff) / 255
	g := float64((n>>8)&0xff) / 255
	b := float64(n&0xff) / 255
	return 0.2126*r + 0.7152*g + 0.0722*b
}

// sortStrings keeps the cycle order stable between runs: a theme cycle whose
// order changes with map iteration is a different keyboard every launch.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// themeCacheDir honours XDG_CACHE_HOME. The cache is the ONLY thing puffin
// writes to disk, and it is sanctioned: themes are the one legal
// last-known-good in this estate.
func themeCacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "puffin")
	}
	return filepath.Join(os.Getenv("HOME"), ".cache", "puffin")
}

// writeThemeCache keeps the last good stylesheet, because a stale colour
// misleads nobody and a missing one leaves the screen unreadable.
func writeThemeCache(css []string) {
	dir := themeCacheDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	for i, p := range dodoThemePaths {
		if i < len(css) {
			_ = os.WriteFile(
				filepath.Join(dir, filepath.Base(p)),
				[]byte(css[i]),
				0o644,
			)
		}
	}
}

// readThemeCache is the second of the three answers, between the network
// and the baked fallback. Which one answered is stated on the roster.
func readThemeCache() ([]string, error) {
	dir := themeCacheDir()
	out := make([]string, 0, len(dodoThemePaths))
	for _, p := range dodoThemePaths {
		b, err := os.ReadFile(filepath.Join(dir, filepath.Base(p)))
		if err != nil {
			return nil, err
		}
		out = append(out, string(b))
	}
	return out, nil
}
