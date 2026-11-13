package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A publication repository may be a personal fork of the project repository.
// The PR destination and the project's local remotes remain unchanged.
type taskPublicationRepositoryProvider interface {
	PreparePublicationRepository(context.Context, *GitHubRepoRef) (*GitHubRepoRef, error)
	ValidatePublicationRepository(context.Context, *GitHubRepoRef, *GitHubRepoRef) error
}

type publicationRepository struct {
	ID            int64  `json:"id"`
	URL           string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Fork          bool   `json:"fork"`
	Source        *struct {
		ID int64 `json:"id"`
	} `json:"source"`
	Permissions *struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

func (r *publicationRepository) networkID() int64 {
	if r.Source != nil {
		return r.Source.ID
	}
	return r.ID
}
func (s *GitHubService) publicationRepository(ctx context.Context, token string, repo *GitHubRepoRef) (*publicationRepository, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s", githubAPIBaseURLForRepo(repo, s.apiBaseURL), url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	var data publicationRepository
	if err := s.doGitHubJSON(req, &data); err != nil {
		return nil, err
	}
	return &data, nil
}
func (s *GitHubService) ValidatePublicationRepository(ctx context.Context, project, head *GitHubRepoRef) error {
	if strings.EqualFold(project.FullName, head.FullName) {
		return nil
	}
	if _, err := SameHostGitHubRepo(head.HTMLURL, project); err != nil {
		return err
	}
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(project, s.apiBaseURL))
	if err != nil {
		return err
	}
	source, err := s.publicationRepository(ctx, token, project)
	if err != nil {
		return err
	}
	fork, err := s.publicationRepository(ctx, token, head)
	if err != nil {
		return err
	}
	if !fork.Fork || source.networkID() == 0 || fork.networkID() != source.networkID() {
		return fmt.Errorf("linked PR source is not a fork of the project repository")
	}
	return nil
}

func (s *GitHubService) PreparePublicationRepository(ctx context.Context, project *GitHubRepoRef) (*GitHubRepoRef, error) {
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(project, s.apiBaseURL))
	if err != nil {
		return nil, err
	}
	source, err := s.publicationRepository(ctx, token, project)
	if err != nil {
		return nil, err
	}
	// Installation tokens may omit user permissions. Retain their existing path.
	if source.Permissions == nil || source.Permissions.Push {
		return project, nil
	}
	base := githubAPIBaseURLForRepo(project, s.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	var user struct {
		Login string `json:"login"`
	}
	if err := s.doGitHubJSON(req, &user); err != nil {
		return nil, fmt.Errorf("finding your GitHub account for fork publication: %w", err)
	}
	if user.Login == "" {
		return nil, fmt.Errorf("GitHub did not return your account login")
	}
	// Search owned repositories, including renamed forks, before creating anything.
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s/user/repos?affiliation=owner&per_page=100&page=%d", base, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		s.applyGitHubHeaders(req, token)
		var repos []publicationRepository
		if err := s.doGitHubJSON(req, &repos); err != nil {
			return nil, err
		}
		for _, candidate := range repos {
			if !candidate.Fork {
				continue
			}
			ref, err := SameHostGitHubRepo(candidate.URL, project)
			if err != nil {
				return nil, err
			}
			if !strings.EqualFold(ref.Owner, user.Login) {
				continue
			}
			detail, err := s.publicationRepository(ctx, token, ref)
			if err != nil {
				return nil, err
			}
			if source.networkID() != 0 && detail.networkID() == source.networkID() && detail.Permissions != nil && detail.Permissions.Push {
				return s.waitForPublicationRepository(ctx, token, ref, source.networkID())
			}
		}
		if len(repos) < 100 {
			break
		}
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/forks", base, url.PathEscape(project.Owner), url.PathEscape(project.Name))
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"default_branch_only":false}`))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	s.applyGitHubHeaders(req, token)
	var fork publicationRepository
	if err := s.doGitHubJSON(req, &fork); err != nil {
		return nil, fmt.Errorf("creating your fork (your GitHub token must allow fork creation): %w", err)
	}
	ref, err := SameHostGitHubRepo(fork.URL, project)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(ref.Owner, user.Login) {
		return nil, fmt.Errorf("GitHub returned a fork owned by another account")
	}
	return s.waitForPublicationRepository(ctx, token, ref, source.networkID())
}

func (s *GitHubService) waitForPublicationRepository(ctx context.Context, token string, ref *GitHubRepoRef, networkID int64) (*GitHubRepoRef, error) {
	// Fork creation is asynchronous; wait until its initial Git objects exist.
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		detail, err := s.publicationRepository(readyCtx, token, ref)
		if err == nil && detail.Fork && networkID != 0 && detail.networkID() == networkID && detail.Permissions != nil && detail.Permissions.Push {
			if _, err = s.githubBranchCommitSHA(readyCtx, token, ref, detail.DefaultBranch); err == nil {
				return ref, nil
			}
		}
		if err != nil && !isPublicationRepositoryPending(err) && readyCtx.Err() == nil {
			return nil, fmt.Errorf("checking your fork: %w", err)
		}
		select {
		case <-readyCtx.Done():
			return nil, fmt.Errorf("your fork is still being prepared by GitHub; retry Create PR shortly: %w", readyCtx.Err())
		case <-time.After(time.Second):
		}
	}
}

// A newly created fork can expose its metadata before its Git objects exist.
// Other conflicts and permission failures must not be hidden by polling.
func isPublicationRepositoryPending(err error) bool {
	if isGitHubNotFoundError(err) {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	return message == "github api request failed (409): git repository is empty." || message == "github api request failed (409): git repository is empty"
}
