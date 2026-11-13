package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitHubRepositoryOptionsProvider supplies fork ancestry and paged target branches.
type GitHubRepositoryOptionsProvider interface {
	RepositoryOptions(context.Context, *GitHubRepoRef) (*GitHubRepositoryOptions, error)
	RepositoryBranches(context.Context, *GitHubRepoRef, int) ([]string, bool, error)
}

type GitHubRepositoryOptions struct {
	DefaultBranch string         `json:"default_branch"`
	Parent        *GitHubRepoRef `json:"parent,omitempty"`
}

func (s *GitHubService) RepositoryOptions(ctx context.Context, repo *GitHubRepoRef) (*GitHubRepositoryOptions, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(repo, s.apiBaseURL))
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s", githubAPIBaseURLForRepo(repo, s.apiBaseURL), url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	var data struct {
		DefaultBranch string `json:"default_branch"`
		Fork          bool   `json:"fork"`
		Parent        *struct {
			URL string `json:"html_url"`
		} `json:"parent"`
	}
	if err = s.doGitHubJSON(req, &data); err != nil {
		return nil, err
	}
	result := &GitHubRepositoryOptions{DefaultBranch: data.DefaultBranch}
	if data.Fork && data.Parent != nil {
		parent, err := SameHostGitHubRepo(data.Parent.URL, repo)
		if err != nil {
			return nil, err
		}
		result.Parent = parent
	}
	return result, nil
}

func (s *GitHubService) RepositoryBranches(ctx context.Context, repo *GitHubRepoRef, page int) ([]string, bool, error) {
	if repo == nil {
		return nil, false, fmt.Errorf("repository reference is required")
	}
	if page < 1 {
		page = 1
	}
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(repo, s.apiBaseURL))
	if err != nil {
		return nil, false, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/branches?per_page=100&page=%d", githubAPIBaseURLForRepo(repo, s.apiBaseURL), url.PathEscape(repo.Owner), url.PathEscape(repo.Name), page)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	s.applyGitHubHeaders(req, token)
	var data []struct {
		Name string `json:"name"`
	}
	if err = s.doGitHubJSON(req, &data); err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(data))
	for _, b := range data {
		names = append(names, b.Name)
	}
	return names, len(data) == 100, nil
}

// SameHostGitHubRepo never accepts an API endpoint supplied by a client or remote payload.
func SameHostGitHubRepo(raw string, source *GitHubRepoRef) (*GitHubRepoRef, error) {
	target, err := ParseGitHubRepoURL(raw)
	if err != nil {
		return nil, err
	}
	host, err := githubRepositoryWebHost(source)
	if err != nil {
		return nil, err
	}
	other, err := githubRepositoryWebHost(&target)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(host, other) {
		return nil, fmt.Errorf("pull request repositories must use the same GitHub host")
	}
	target.APIBaseURL = source.APIBaseURL
	return &target, nil
}

// TaskPullRequestTarget resolves the immutable target of an already linked PR.
func TaskPullRequestTarget(raw string, number int, source *GitHubRepoRef) (*GitHubRepoRef, error) {
	if strings.TrimSpace(raw) == "" {
		return source, nil
	} // Legacy records predate stored PR URLs.
	return parsePersistedGitHubPullRequestURL(raw, number, source)
}

// ValidatePullRequestBase checks the destination before publishing a branch.
func (s *GitHubService) ValidatePullRequestBase(ctx context.Context, repo *GitHubRepoRef, branch string) error {
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(repo, s.apiBaseURL))
	if err != nil {
		return err
	}
	_, err = s.githubBranchCommitSHA(ctx, token, repo, branch)
	if err != nil {
		return fmt.Errorf("checking PR target branch %q: %w", branch, err)
	}
	return nil
}
