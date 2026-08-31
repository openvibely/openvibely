package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

func TestAuthorizedUserCRUDOrchestration(t *testing.T) {
	newContext := func() (echo.Context, *httptest.ResponseRecorder) {
		e := echo.New()
		rec := httptest.NewRecorder()
		return e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec), rec
	}
	assertHTTPError := func(t *testing.T, err error, code int, message string) {
		t.Helper()
		httpErr, ok := err.(*echo.HTTPError)
		if !ok {
			t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
		}
		if httpErr.Code != code || httpErr.Message != message {
			t.Fatalf("expected %d %q, got %d %q", code, message, httpErr.Code, httpErr.Message)
		}
	}
	newCRUD := func(list func(context.Context, string) ([]string, error)) authorizedUserCRUD[string] {
		return authorizedUserCRUD[string]{
			list: list,
			render: func(users []string, projectID string) templ.Component {
				return templ.Raw(projectID + ":" + strings.Join(users, ","))
			},
		}
	}

	t.Run("project lookup adapts typed records", func(t *testing.T) {
		type authRecord struct{ ProjectID string }
		lookup := authorizedUserProjectLookup(func(ctx context.Context, id string) (*authRecord, error) {
			if id != "user-1" {
				t.Fatalf("expected lookup id user-1, got %q", id)
			}
			return &authRecord{ProjectID: "record-project"}, nil
		}, func(record *authRecord) string {
			return record.ProjectID
		})
		projectID, found, err := lookup(context.Background(), "user-1")
		if err != nil || !found || projectID != "record-project" {
			t.Fatalf("expected found record-project without error, got project=%q found=%v err=%v", projectID, found, err)
		}

		lookup = authorizedUserProjectLookup(func(context.Context, string) (*authRecord, error) {
			return nil, nil
		}, func(record *authRecord) string { return record.ProjectID })
		projectID, found, err = lookup(context.Background(), "missing")
		if err != nil || found || projectID != "" {
			t.Fatalf("expected missing record without error, got project=%q found=%v err=%v", projectID, found, err)
		}

		lookup = authorizedUserProjectLookup(func(context.Context, string) (*authRecord, error) {
			return &authRecord{ProjectID: "record-project"}, errors.New("lookup failed")
		}, func(record *authRecord) string { return record.ProjectID })
		projectID, found, err = lookup(context.Background(), "user-1")
		if err == nil || !found || projectID != "" {
			t.Fatalf("expected lookup error with found record marker, got project=%q found=%v err=%v", projectID, found, err)
		}
	})

	t.Run("list validates project and maps repository failure", func(t *testing.T) {
		c, _ := newContext()
		crud := newCRUD(func(context.Context, string) ([]string, error) {
			t.Fatal("list should not run without a project")
			return nil, nil
		})
		assertHTTPError(t, crud.listUsers(c, ""), http.StatusBadRequest, "project_id is required")

		crud = newCRUD(func(context.Context, string) ([]string, error) { return nil, errors.New("database unavailable") })
		assertHTTPError(t, crud.listUsers(c, "project-1"), http.StatusInternalServerError, "Failed to load authorized users")
	})

	t.Run("create mutates then reloads", func(t *testing.T) {
		c, rec := newContext()
		created := false
		crud := newCRUD(func(_ context.Context, projectID string) ([]string, error) {
			if !created {
				t.Fatal("list called before create")
			}
			return []string{"user-1"}, nil
		})
		if err := crud.createUser(c, "project-1", func(context.Context) error {
			created = true
			return nil
		}); err != nil {
			t.Fatalf("createUser failed: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "project-1:user-1" {
			t.Fatalf("unexpected create response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("create validates and maps mutation and reload failures", func(t *testing.T) {
		c, _ := newContext()
		crud := newCRUD(func(context.Context, string) ([]string, error) { return nil, nil })
		assertHTTPError(t, crud.createUser(c, "", func(context.Context) error {
			t.Fatal("create should not run without a project")
			return nil
		}), http.StatusBadRequest, "project_id is required")
		assertHTTPError(t, crud.createUser(c, "project-1", func(context.Context) error {
			return errors.New("insert failed")
		}), http.StatusInternalServerError, "Failed to add authorized user: insert failed")

		crud = newCRUD(func(context.Context, string) ([]string, error) { return nil, errors.New("reload failed") })
		assertHTTPError(t, crud.createUser(c, "project-1", func(context.Context) error { return nil }), http.StatusInternalServerError, "Failed to load authorized users")
	})

	t.Run("delete falls back to loaded project then reloads", func(t *testing.T) {
		c, rec := newContext()
		deleted := false
		crud := newCRUD(func(_ context.Context, projectID string) ([]string, error) {
			if projectID != "record-project" {
				t.Fatalf("expected record project fallback, got %q", projectID)
			}
			if !deleted {
				t.Fatal("list called before delete")
			}
			return nil, nil
		})
		err := crud.deleteUser(c, "user-1", "", func(_ context.Context, id string) (string, bool, error) {
			return "record-project", true, nil
		}, func(_ context.Context, id string) error {
			deleted = true
			return nil
		})
		if err != nil {
			t.Fatalf("deleteUser failed: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "record-project:" {
			t.Fatalf("unexpected delete response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("delete maps lookup not found mutation and reload failures", func(t *testing.T) {
		c, _ := newContext()
		crud := newCRUD(func(context.Context, string) ([]string, error) { return nil, nil })
		assertHTTPError(t, crud.deleteUser(c, "user-1", "project-1", func(context.Context, string) (string, bool, error) {
			return "", false, errors.New("lookup failed")
		}, func(context.Context, string) error { return nil }), http.StatusInternalServerError, "Failed to find user")
		assertHTTPError(t, crud.deleteUser(c, "user-1", "project-1", func(context.Context, string) (string, bool, error) {
			return "", false, nil
		}, func(context.Context, string) error { return nil }), http.StatusNotFound, "User not found")
		assertHTTPError(t, crud.deleteUser(c, "user-1", "project-1", func(context.Context, string) (string, bool, error) {
			return "record-project", true, nil
		}, func(context.Context, string) error { return errors.New("delete failed") }), http.StatusInternalServerError, "Failed to remove user: delete failed")

		crud = newCRUD(func(context.Context, string) ([]string, error) { return nil, errors.New("reload failed") })
		assertHTTPError(t, crud.deleteUser(c, "user-1", "project-1", func(context.Context, string) (string, bool, error) {
			return "record-project", true, nil
		}, func(context.Context, string) error { return nil }), http.StatusInternalServerError, "Failed to load authorized users")
	})

	t.Run("adapter guard returns channel not configured message", func(t *testing.T) {
		c, _ := newContext()
		crud := authorizedUserCRUD[string]{notConfiguredMessage: "Channel auth not configured"}
		assertHTTPError(t, crud.listHandler(c), http.StatusInternalServerError, "Channel auth not configured")
		assertHTTPError(t, crud.createHandler(c, func(context.Context, string) error {
			t.Fatal("create should not run when repository is missing")
			return nil
		}), http.StatusInternalServerError, "Channel auth not configured")
	})

	t.Run("adapter reads list query project", func(t *testing.T) {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?project_id=query-project", nil), rec)
		crud := newCRUD(func(_ context.Context, projectID string) ([]string, error) {
			if projectID != "query-project" {
				t.Fatalf("expected query project, got %q", projectID)
			}
			return []string{"user-1"}, nil
		})
		crud.configured = true
		if err := crud.listHandler(c); err != nil {
			t.Fatalf("listHandler failed: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "query-project:user-1" {
			t.Fatalf("unexpected list response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("adapter reads create form project and reloads", func(t *testing.T) {
		e := echo.New()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("project_id=form-project"))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		c := e.NewContext(req, rec)
		created := false
		crud := newCRUD(func(_ context.Context, projectID string) ([]string, error) {
			if projectID != "form-project" {
				t.Fatalf("expected form project reload, got %q", projectID)
			}
			if !created {
				t.Fatal("list called before create")
			}
			return []string{"user-1"}, nil
		})
		crud.configured = true
		if err := crud.createHandler(c, func(_ context.Context, projectID string) error {
			if projectID != "form-project" {
				t.Fatalf("expected form project create, got %q", projectID)
			}
			created = true
			return nil
		}); err != nil {
			t.Fatalf("createHandler failed: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "form-project:user-1" {
			t.Fatalf("unexpected create response: %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("adapter wires delete lookup and fallback project", func(t *testing.T) {
		type authRecord struct{ ProjectID string }
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodDelete, "/authorized/user-1", nil), rec)
		c.SetParamNames("id")
		c.SetParamValues("user-1")
		deleted := false
		crud := authorizedUserCRUD[authRecord]{
			list: func(_ context.Context, projectID string) ([]authRecord, error) {
				if projectID != "record-project" {
					t.Fatalf("expected record project fallback, got %q", projectID)
				}
				if !deleted {
					t.Fatal("list called before delete")
				}
				return nil, nil
			},
			render: func(_ []authRecord, projectID string) templ.Component {
				return templ.Raw(projectID)
			},
			getByID: func(_ context.Context, id string) (*authRecord, error) {
				if id != "user-1" {
					t.Fatalf("expected lookup id user-1, got %q", id)
				}
				return &authRecord{ProjectID: "record-project"}, nil
			},
			delete: func(_ context.Context, id string) error {
				if id != "user-1" {
					t.Fatalf("expected delete id user-1, got %q", id)
				}
				deleted = true
				return nil
			},
			projectID:  func(record *authRecord) string { return record.ProjectID },
			configured: true,
		}
		if err := crud.deleteHandler(c); err != nil {
			t.Fatalf("deleteHandler failed: %v", err)
		}
		if rec.Code != http.StatusOK || rec.Body.String() != "record-project" {
			t.Fatalf("unexpected delete response: %d %q", rec.Code, rec.Body.String())
		}
	})
}
