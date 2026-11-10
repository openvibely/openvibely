package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAutomaticPublicationRepository(t *testing.T) {
	for _, mode := range []string{"direct", "existing-renamed-fork", "create", "create-wait", "existing-pending", "create-empty", "existing-empty", "conflict", "denied"} {
		t.Run(mode, func(t *testing.T) {
			creates := 0
			branchReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/repos/upstream/project":
					fmt.Fprintf(w, `{"id":1,"permissions":{"push":%t}}`, mode == "direct")
				case "/user":
					fmt.Fprint(w, `{"login":"alice"}`)
				case "/user/repos":
					if mode == "existing-renamed-fork" || mode == "existing-pending" || mode == "existing-empty" || mode == "conflict" {
						fmt.Fprint(w, `[{"fork":true,"html_url":"https://github.com/alice/renamed"}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case "/repos/upstream/project/forks":
					if r.Method != http.MethodPost {
						t.Error("expected POST")
					}
					creates++
					if mode == "denied" {
						w.WriteHeader(403)
						fmt.Fprint(w, `{"message":"Forbidden"}`)
						return
					}
					w.WriteHeader(202)
					fmt.Fprint(w, `{"html_url":"https://github.com/alice/renamed"}`)
				case "/repos/alice/renamed":
					fmt.Fprint(w, `{"id":2,"fork":true,"source":{"id":1},"default_branch":"main","permissions":{"push":true}}`)
				case "/repos/alice/renamed/git/ref/heads/main":
					branchReads++
					if mode == "conflict" {
						w.WriteHeader(409)
						fmt.Fprint(w, `{ "message": "Another conflict" }`)
						return
					}
					if (mode == "create-empty" || mode == "existing-empty") && branchReads == 1 {
						w.WriteHeader(409)
						fmt.Fprint(w, `{ "message": "Git Repository is empty." }`)
						return
					}

					if (mode == "create-wait" || mode == "existing-pending") && branchReads == 1 {
						w.WriteHeader(404)
						fmt.Fprint(w, `{ "message": "Not Found" }`)
						return
					}
					fmt.Fprint(w, `{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			svc := newPATGitHubService(t, server.URL)
			repo, _ := ParseGitHubRepoURL("https://github.com/upstream/project")
			repo.APIBaseURL = server.URL
			got, err := svc.PreparePublicationRepository(t.Context(), &repo)
			if mode == "conflict" {
				if err == nil || branchReads != 1 {
					t.Fatalf("expected immediate conflict failure, reads=%d err=%v", branchReads, err)
				}
				return
			}
			if mode == "denied" {
				if err == nil {
					t.Fatal("expected fork permission error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "alice/renamed"
			if mode == "direct" {
				want = repo.FullName
			}
			if got.FullName != want {
				t.Fatalf("repo=%s want %s", got.FullName, want)
			}
			if mode == "create-wait" || mode == "existing-pending" || mode == "create-empty" || mode == "existing-empty" {
				if branchReads != 2 {
					t.Fatalf("expected readiness retry, got %d branch reads", branchReads)
				}
			}
			wantCreates := 0
			if mode == "create" || mode == "create-wait" || mode == "create-empty" {
				wantCreates = 1
			}
			if creates != wantCreates {
				t.Fatalf("fork creations=%d", creates)
			}
		})
	}
}

func TestValidatePublicationRepositoryRejectsUnrelatedFork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/upstream/project" {
			fmt.Fprint(w, `{"id":1}`)
		} else {
			fmt.Fprint(w, `{"id":3,"fork":true,"source":{"id":99}}`)
		}
	}))
	defer server.Close()
	svc := newPATGitHubService(t, server.URL)
	project, _ := ParseGitHubRepoURL("https://github.com/upstream/project")
	project.APIBaseURL = server.URL
	head, _ := SameHostGitHubRepo("https://github.com/alice/unrelated", &project)
	if err := svc.ValidatePublicationRepository(t.Context(), &project, head); err == nil {
		t.Fatal("accepted unrelated fork")
	}
}
