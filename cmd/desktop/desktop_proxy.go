package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

func desktopAssetProxy(backendURL *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.FlushInterval = -1
	// Keep HttpOnly cookies in the native process: custom-scheme WebViews do
	// not reliably implement Set-Cookie. The jar is private to this window's
	// proxy, never a loopback HTTP endpoint, and expires when the app exits.
	jar, _ := cookiejar.New(nil)
	var cookieMu sync.Mutex
	managed := make(map[string]bool)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		// Identify documents requested through the native window, not the server mode.
		r.Header.Set("X-OpenVibely-Native-Window", "1")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.Request.Context().Value(desktopSessionKey{}) == true {
			cookieMu.Lock()
			for _, cookie := range response.Cookies() {
				if cookie.HttpOnly || managed[cookie.Name] {
					managed[cookie.Name] = true
					jar.SetCookies(response.Request.URL, []*http.Cookie{cookie})
				}
			}
			cookieMu.Unlock()
		}
		if runtime.GOOS == "linux" {
			return desktopNavigationRedirect(response)
		}
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		trusted := desktopTrustedRequest(r)
		if desktopAuthPath(r.URL.Path) && !trusted {
			http.Error(w, "same-origin desktop request required", http.StatusForbidden)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), desktopSessionKey{}, trusted))
		cookieMu.Lock()
		cookies := r.Cookies()
		r.Header.Del("Cookie")
		for _, cookie := range cookies {
			// Never resurrect a managed session from a stale browser cookie,
			// including after logout or expiry.
			if !managed[cookie.Name] {
				r.AddCookie(cookie)
			}
		}
		if trusted {
			cookieURL := *backendURL
			cookieURL.Path = r.URL.Path
			for _, cookie := range jar.Cookies(&cookieURL) {
				r.AddCookie(cookie)
			}
		}
		cookieMu.Unlock()
		// Translate only the authentication/organization origin checks. Other
		// native routes use the original Wails origin for their capabilities.
		if trusted && desktopAuthPath(r.URL.Path) {
			// WebKit omits both headers on same-origin XMLHttpRequest (HTMX).
			// This handler is reached only through Wails, never over HTTP.
			if r.Header.Get("Origin") == "" && r.Header.Get("Referer") == "" {
				r.Header.Set("Origin", backendURL.Scheme+"://"+r.Host)
			}
			for _, header := range []string{"Origin", "Referer"} {
				if raw := r.Header.Get(header); raw != "" {
					u, err := url.Parse(raw)
					if err == nil && desktopOriginMatches(u, r.Host) {
						u.Scheme = backendURL.Scheme
						r.Header.Set(header, u.String())
					}
				}
			}
		}
		// Wails' streaming WebView bodies can omit Content-Length. ReverseProxy
		// drops bodies with length zero, so mark a supplied body as unknown-length
		// before it clones the request (its Director runs too late).
		if r.ContentLength == 0 && r.Body != nil && r.Body != http.NoBody {
			r = r.Clone(r.Context())
			r.ContentLength = -1
			r.Header.Del("Content-Length")
		}
		proxy.ServeHTTP(w, r)
	})
}

// WebKitGTK's custom-scheme transport delivers HTTP redirects as documents
// instead of following them. Navigate from a small document on the Wails origin
// so the browser changes the address normally. The proxy handles session cookies.
// Do not translate fetch/HTMX responses or replay method-preserving POSTs.
func desktopNavigationRedirect(response *http.Response) error {
	request := response.Request
	if request == nil || (request.Method != http.MethodGet && request.Method != http.MethodPost) {
		return nil
	}
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther:
	case http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if request.Method != http.MethodGet {
			return nil
		}
	default:
		return nil
	}
	if request.Header.Get("HX-Request") != "" || request.Header.Get("X-Requested-With") != "" {
		return nil
	}
	if dest := request.Header.Get("Sec-Fetch-Dest"); dest != "" {
		if dest != "document" {
			return nil
		}
	} else if request.Header.Get("Upgrade-Insecure-Requests") != "1" || !strings.Contains(request.Header.Get("Accept"), "text/html") {
		// WebKit does not send Sec-Fetch-* for wails:// navigations. These are
		// the navigation headers observed on both GETs and native form submits.
		return nil
	}
	location := strings.TrimSpace(response.Header.Get("Location"))
	if location == "" {
		return nil
	}
	target, err := url.Parse(location)
	if err != nil || target.User != nil {
		return nil
	}
	switch strings.ToLower(target.Scheme) {
	case "", "http", "https":
	case "wails":
		if target.Host != "localhost" {
			return nil
		}
	default:
		// A network redirect cannot execute javascript: or data: URLs. Preserve
		// that boundary when converting the response to a script navigation.
		return nil
	}
	if target.Scheme == request.URL.Scheme && target.Host == request.URL.Host {
		// Backend-generated absolute URLs must stay on the native Wails origin.
		target.Scheme, target.Host = "wails", "localhost"
		location = target.String()
	}
	encoded, _ := json.Marshal(location) // Escapes quotes and HTML script delimiters.
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><title>OpenVibely</title></head><body><script>window.location.replace(" + string(encoded) + ");</script></body></html>"
	if response.Body != nil {
		_ = response.Body.Close()
	}
	response.Body = io.NopCloser(strings.NewReader(body))
	response.StatusCode = http.StatusOK
	response.Status = "200 OK"
	response.ContentLength = int64(len(body))
	response.TransferEncoding = nil
	for _, name := range []string{"Location", "Content-Encoding", "Content-Range", "Content-Disposition", "ETag", "Last-Modified", "Refresh", "Transfer-Encoding"} {
		response.Header.Del(name)
	}
	response.Header.Set("Content-Type", "text/html; charset=utf-8")
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Set("Cache-Control", "no-store")
	return nil
}

type desktopSessionKey struct{}

// Trust only the native document's exact origin. An opaque sandbox (Origin:
// null), external page, or duplicate Origin must never borrow the native session.
func desktopTrustedRequest(r *http.Request) bool {
	if r == nil || (r.Host != "localhost" && r.Host != "wails.localhost") {
		return false
	}
	values := r.Header.Values("Origin")
	if len(values) > 1 {
		return false
	}
	if len(values) == 1 {
		u, err := url.Parse(values[0])
		return err == nil && desktopOriginMatches(u, r.Host) && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
	}
	if raw := r.Header.Get("Referer"); raw != "" {
		u, err := url.Parse(raw)
		return err == nil && desktopOriginMatches(u, r.Host)
	}
	// Same-origin WebKit XHR can omit Origin and Referer entirely. Cross-origin
	// and opaque requests carry an Origin; also reject explicit fetch metadata.
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func desktopOriginMatches(u *url.URL, host string) bool {
	if u == nil || u.User != nil || u.Host != host {
		return false
	}
	switch host {
	case "localhost":
		return u.Scheme == "wails"
	case "wails.localhost":
		return u.Scheme == "http" || u.Scheme == "https"
	}
	return false
}

func desktopAuthPath(path string) bool {
	return path == "/login" || path == "/logout" || path == "/invite" || path == "/organization" || strings.HasPrefix(path, "/organization/")
}
