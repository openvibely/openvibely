package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
)

func navigationRequest(method string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:1234/login", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	return req
}

func redirectResponse(req *http.Request, status int, location string) *http.Response {
	return &http.Response{Request: req, StatusCode: status, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("redirect"))}
}

func TestDesktopNavigationRedirect(t *testing.T) {
	for _, test := range []struct {
		name, method   string
		status         int
		location, want string
	}{
		{"login", "POST", 302, "/", "/"},
		{"logout", "POST", 302, "/login", "/login"},
		{"invite", "POST", 302, "/organization", "/organization"},
		{"create page", "POST", 303, "/tasks/example?project_id=one", "/tasks/example?project_id=one"},
		{"root", "GET", 303, "/chat", "/chat"},
		{"permanent", "GET", 301, "/chat", "/chat"},
		{"temporary GET", "GET", 307, "/chat", "/chat"},
		{"permanent GET", "GET", 308, "/chat", "/chat"},
		{"relative", "GET", 302, "../chat?project_id=one#latest", "../chat?project_id=one#latest"},
		{"backend absolute", "GET", 302, "http://127.0.0.1:1234/chat?q=one#latest", "wails://localhost/chat?q=one#latest"},
		{"backend double slash", "GET", 302, "http://127.0.0.1:1234//example.com", "wails://localhost//example.com"},
		{"external HTTPS", "GET", 302, "https://example.com/login", "https://example.com/login"},
		{"script injection", "GET", 302, `/chat?q=</script><script>alert("x")</script>`, `/chat?q=</script><script>alert("x")</script>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := redirectResponse(navigationRequest(test.method), test.status, test.location)
			res.Header.Add("Set-Cookie", "session=test; HttpOnly; SameSite=Lax")
			res.Header.Add("Set-Cookie", "old=; Max-Age=0")
			res.Header.Set("Content-Encoding", "gzip")
			res.Header.Set("Content-Length", "8")
			res.Header.Set("Referrer-Policy", "no-referrer")
			if err := desktopNavigationRedirect(res); err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 {
				t.Fatalf("status=%d", res.StatusCode)
			}
			encoded, _ := json.Marshal(test.want)
			if !strings.Contains(string(body), "window.location.replace("+string(encoded)+");") {
				t.Fatalf("wrong navigation: %s", body)
			}
			if strings.Count(string(body), "<script>") != 1 || strings.Count(string(body), "</script>") != 1 {
				t.Fatalf("unsafe script: %s", body)
			}
			if len(res.Header.Values("Set-Cookie")) != 2 || res.Header.Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("lost cookies or security headers")
			}
			if res.Header.Get("Location") != "" || res.Header.Get("Content-Encoding") != "" || res.ContentLength != int64(len(body)) || res.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("invalid redirect document headers")
			}
		})
	}
}

func TestDesktopNavigationRedirectLeavesOtherResponsesAlone(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*http.Response)
	}{
		{"HTMX", func(r *http.Response) { r.Request.Header.Set("HX-Request", "true") }},
		{"XHR", func(r *http.Response) { r.Request.Header.Set("X-Requested-With", "XMLHttpRequest") }},
		{"fetch", func(r *http.Response) { r.Request.Header.Del("Upgrade-Insecure-Requests") }},
		{"fetch metadata", func(r *http.Response) { r.Request.Header.Set("Sec-Fetch-Dest", "empty") }},
		{"iframe", func(r *http.Response) { r.Request.Header.Set("Sec-Fetch-Dest", "iframe") }},
		{"asset", func(r *http.Response) { r.Request.Header.Set("Accept", "*/*") }},
		{"POST 307", func(r *http.Response) { r.StatusCode = 307 }},
		{"POST 308", func(r *http.Response) { r.StatusCode = 308 }},
		{"HEAD", func(r *http.Response) { r.Request.Method = "HEAD" }},
		{"not redirect", func(r *http.Response) { r.StatusCode = 200 }},
		{"no location", func(r *http.Response) { r.Header.Del("Location") }},
		{"javascript", func(r *http.Response) { r.Header.Set("Location", "JaVaScRiPt:alert(1)") }},
		{"data", func(r *http.Response) { r.Header.Set("Location", "data:text/html,test") }},
		{"malformed", func(r *http.Response) { r.Header.Set("Location", "/%zz") }},
		{"credentials", func(r *http.Response) { r.Header.Set("Location", "https://user:pass@example.com") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			res := redirectResponse(navigationRequest("POST"), 302, "/chat")
			test.change(res)
			originalBody, status, location := res.Body, res.StatusCode, res.Header.Get("Location")
			if err := desktopNavigationRedirect(res); err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.Body != originalBody || res.StatusCode != status || res.Header.Get("Location") != location {
				t.Fatal("non-navigation response was changed")
			}
		})
	}
}

func TestDesktopProxyRedirectDoesNotReplayForm(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux custom-scheme workaround")
	}
	calls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/login" || r.FormValue("username") != "test" {
			t.Errorf("unexpected backend request: %s %s", r.Method, r.URL)
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "test", HttpOnly: true})
		http.Redirect(w, r, "/chat", 302)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	req := navigationRequest("POST")
	req.Body = io.NopCloser(strings.NewReader("username=test"))
	req.ContentLength = 0 // Native streaming request body.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	desktopAssetProxy(target).ServeHTTP(rec, req)
	if calls != 1 || rec.Code != 200 || len(rec.Result().Cookies()) != 1 || !strings.Contains(rec.Body.String(), `window.location.replace("/chat")`) {
		t.Fatalf("calls=%d status=%d response=%s", calls, rec.Code, rec.Body.String())
	}
}
