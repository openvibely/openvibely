// Package static serves the front-end assets built by scripts/build-assets.sh and embedded
// in the binary, so pages never depend on third-party CDNs.
package static

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

// Prefix is the URL path assets are served under.
const Prefix = "/static/"

//go:embed dist
var embedded embed.FS

var dist = mustSub(embedded, "dist")

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

var (
	hashesOnce sync.Once
	hashes     map[string]string
)

// URL returns the path for an asset with a content-hash query, so browsers can cache it
// for a long time and still pick up a new build. name is relative to dist, e.g.
// "app.css" or "vendor/htmx.min.js". It panics on a missing asset, which a test covers.
func URL(name string) string {
	hashesOnce.Do(func() {
		hashes = map[string]string{}
		_ = fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(dist, p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			hashes[p] = hex.EncodeToString(sum[:])[:12]
			return nil
		})
	})
	hash, ok := hashes[name]
	if !ok {
		panic("static: unknown asset " + name)
	}
	return Prefix + name + "?v=" + hash
}

var fileServer = http.StripPrefix(strings.TrimSuffix(Prefix, "/"), http.FileServer(http.FS(dist)))

// Handler serves embedded assets under Prefix. Requests carrying a content-hash query are
// cached for a year; the URL changes whenever the file does.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), Prefix)
		if name == "" || strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ServeAsset serves r from the embedded assets when its path is under Prefix and reports
// whether it did. Test fixture servers use it to serve the pages' assets.
func ServeAsset(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, Prefix) {
		return false
	}
	Handler().ServeHTTP(w, r)
	return true
}
