package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

func desktopAssetProxy(backendURL *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.FlushInterval = -1
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		// Identify documents requested through the native window, not the server mode.
		r.Header.Set("X-OpenVibely-Native-Window", "1")
	}
	if runtime.GOOS == "linux" {
		proxy.ModifyResponse = desktopNavigationRedirect
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
// so the browser processes response cookies and changes the address normally.
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
