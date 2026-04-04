package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestPickProjectFolder_Success(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")
	h, e, _ := setupTestHandler(t)
	h.SetProjectFolderPicker(func(ctx context.Context) (string, bool, error) {
		if runtime.GOOS == "windows" {
			return `C:\\Users\\test\\repo\\`, false, nil
		}
		return "/Users/test/repo/", false, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/pick-folder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `"selected":true`)
	if runtime.GOOS == "windows" {
		assertContains(t, rec, `"C:\\Users\\test\\repo"`)
	} else {
		assertContains(t, rec, `"/Users/test/repo"`)
	}
}

func TestPickProjectFolder_Canceled(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")
	h, e, _ := setupTestHandler(t)
	h.SetProjectFolderPicker(func(ctx context.Context) (string, bool, error) {
		return "", true, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/pick-folder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, `"selected":false`)
	assertContains(t, rec, `"canceled":true`)
}

func TestPickProjectFolder_NotImplementedWhenUnavailable(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")
	h, e, _ := setupTestHandler(t)
	h.SetProjectFolderPicker(func(ctx context.Context) (string, bool, error) {
		return "", false, errProjectFolderPickerUnavailable
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/pick-folder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusNotImplemented)
	assertContains(t, rec, `"selected":false`)
	assertContains(t, rec, `"Native folder picker is unavailable on this system. Paste an absolute path manually."`)
}

func TestPickProjectFolder_RejectsNonAbsoluteSelection(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "true")
	h, e, _ := setupTestHandler(t)
	h.SetProjectFolderPicker(func(ctx context.Context) (string, bool, error) {
		return "relative/path", false, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/projects/pick-folder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusInternalServerError)
	assertContains(t, rec, "native folder picker returned a non-absolute path")
}

func TestPickProjectFolder_LocalModeDisabled(t *testing.T) {
	t.Setenv("OPENVIBELY_ENABLE_LOCAL_REPO_PATH", "false")
	t.Setenv("ENVIRONMENT", "production")

	h, e, _ := setupTestHandler(t)
	h.SetLocalRepoPathEnabled(false)
	req := httptest.NewRequest(http.MethodPost, "/projects/pick-folder", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "Local repository paths are disabled in this environment") {
		t.Fatalf("expected disabled-mode error message, got: %s", rec.Body.String())
	}
}
