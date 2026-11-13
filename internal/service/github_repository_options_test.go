package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubForkDiscoveryBranchesAndQualifiedHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/contributor/fork":
			fmt.Fprint(w, `{"fork":true,"default_branch":"main","parent":{"html_url":"https://github.com/maintainer/original"}}`)
		case r.URL.Path == "/repos/maintainer/original/branches":
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("missing page size")
			}
			if r.URL.Query().Get("page") == "1" {
				items := []map[string]string{}
				for i := 0; i < 100; i++ {
					items = append(items, map[string]string{"name": fmt.Sprintf("branch/%d", i)})
				}
				json.NewEncoder(w).Encode(items)
			} else {
				fmt.Fprint(w, `[{"name":"last"}]`)
			}
		case r.URL.Path == "/repos/maintainer/original/pulls" && r.Method == http.MethodGet:
			if r.URL.Query().Get("head") != "contributor:task/example" {
				t.Errorf("incorrect head query: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("base") != "" {
				fmt.Fprint(w, `[{"number":1,"state":"open","base":{"ref":"main"},"head":{"ref":"task/example","repo":{"full_name":"contributor/other-fork"}}},{"number":2,"state":"open","base":{"ref":"main"},"head":{"ref":"task/example","repo":{"full_name":"contributor/fork"}}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.URL.Path == "/repos/maintainer/original/pulls" && r.Method == http.MethodPost:
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["head"] != "contributor:task/example" || body["head_repo"] != "fork" || body["base"] != "main" {
				t.Errorf("incorrect PR payload: %+v", body)
			}
			fmt.Fprint(w, `{"number":12,"html_url":"https://github.com/maintainer/original/pull/12","state":"open"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	svc := newPATGitHubService(t, server.URL)
	source, _ := ParseGitHubRepoURL("https://github.com/contributor/fork")
	source.APIBaseURL = server.URL
	metadata, err := svc.RepositoryOptions(t.Context(), &source)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Parent == nil || metadata.Parent.FullName != "maintainer/original" || metadata.Parent.APIBaseURL != server.URL {
		t.Fatalf("invalid parent: %+v", metadata)
	}
	branches, more, err := svc.RepositoryBranches(t.Context(), metadata.Parent, 1)
	if err != nil || !more || len(branches) != 100 {
		t.Fatalf("first page: %v %v %v", branches, more, err)
	}
	branches, more, err = svc.RepositoryBranches(t.Context(), metadata.Parent, 2)
	if err != nil || more || len(branches) != 1 {
		t.Fatalf("last page: %v %v %v", branches, more, err)
	}
	if _, err = svc.FindPullRequestByBranch(t.Context(), metadata.Parent, "contributor:task/example"); err != nil {
		t.Fatal(err)
	}
	matched, err := svc.FindPullRequestForTarget(t.Context(), metadata.Parent, "contributor:task/example", "contributor/fork", "main")
	if err != nil || matched == nil || matched.Number != 2 {
		t.Fatalf("scoped PR lookup = %+v, %v", matched, err)
	}

	if _, err = svc.CreatePullRequest(t.Context(), metadata.Parent, GitHubCreatePullRequestRequest{Title: "Example", Head: "contributor:task/example", HeadRepo: "fork", Base: "main"}); err != nil {
		t.Fatal(err)
	}
}
