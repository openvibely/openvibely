package service

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/pkg/gitidentity"
)

func (s *GitHubService) SetPersonalConnections(store persistence.GitHubConnectionStore) {
	s.personalConnections = store
	runner := s.runGit
	s.runGit = func(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
		return runner(s.GitContext(ctx), dir, env, args...)
	}
}
func (s *GitHubService) personalContext(ctx context.Context) context.Context {
	if _, ok := models.PrincipalFromContext(ctx); !ok && s.credentialAuthOff {
		return models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	}
	return ctx
}
func (s *GitHubService) PersonalConnection(ctx context.Context) (*models.GitHubConnection, error) {
	if s.personalConnections == nil {
		return nil, fmt.Errorf("personal GitHub connections are not configured")
	}
	return s.personalConnections.GetOwn(s.personalContext(ctx))
}
func (s *GitHubService) SavePersonalConnection(ctx context.Context, c models.GitHubConnection, revision int64) (bool, error) {
	c.AuthorName = strings.TrimSpace(c.AuthorName)
	c.AuthorEmail = strings.TrimSpace(c.AuthorEmail)
	if strings.ContainsAny(c.AuthorName, "\r\n\x00<>") {
		return false, fmt.Errorf("commit author name must not contain control characters or angle brackets")
	}
	address, err := mail.ParseAddress(c.AuthorEmail)
	if c.AuthorEmail != "" && (err != nil || address.Address != c.AuthorEmail || strings.ContainsAny(c.AuthorEmail, "\r\n\x00")) {
		return false, fmt.Errorf("a valid commit email is required")
	}
	c.APIEndpoint, err = personalGitHubAPIEndpoint(c.APIEndpoint)
	if err != nil {
		return false, err
	}
	c.PAT = strings.TrimSpace(c.PAT)
	if c.PAT == "" {
		return false, fmt.Errorf("GitHub personal access token is required")
	}
	if s.personalConnections == nil {
		return false, fmt.Errorf("personal GitHub connections are not configured")
	}
	return s.personalConnections.SaveOwn(s.personalContext(ctx), c, revision)
}
func (s *GitHubService) DeletePersonalConnection(ctx context.Context, revision int64) (bool, error) {
	if s.personalConnections == nil {
		return false, fmt.Errorf("personal GitHub connections are not configured")
	}
	return s.personalConnections.DeleteOwn(s.personalContext(ctx), revision)
}
func personalGitHubAPIEndpoint(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "https://api.github.com", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("GitHub API endpoint must be an HTTPS URL without credentials, query, or fragment")
	}
	return raw, nil
}

// LookupPersonalGitHubIdentity never saves a supplied token. A missing email
// permission leaves the editable email field empty instead of inventing one.
func (s *GitHubService) LookupPersonalGitHubIdentity(ctx context.Context, token, endpoint string) (*models.GitHubConnection, error) {
	if _, err := s.PersonalConnection(ctx); err != nil {
		return nil, err
	}
	base, err := personalGitHubAPIEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("GitHub personal access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	var profile struct {
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := s.doGitHubJSON(req, &profile); err != nil {
		return nil, fmt.Errorf("could not look up GitHub identity; check the token and endpoint")
	}
	result := &models.GitHubConnection{Login: profile.Login, AuthorName: profile.Name, AuthorEmail: profile.Email, APIEndpoint: base}
	if result.AuthorName == "" {
		result.AuthorName = profile.Login
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/user/emails", nil)
	if err != nil {
		return result, nil
	}
	s.applyGitHubHeaders(req, token)
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if s.doGitHubJSON(req, &emails) == nil {
		for _, email := range emails {
			if email.Primary && email.Verified {
				result.AuthorEmail = email.Email
				break
			}
		}
	}
	return result, nil
}

// GitContext installs a lazy resolver so queued operations use their restored
// principal, never the user who happened to construct the service.
func (s *GitHubService) GitContext(ctx context.Context) context.Context {
	if s.personalConnections == nil {
		return ctx
	}
	appCfg, configErr := s.getAppConfig(ctx)
	mode, modeErr := s.resolveAuthMode(ctx, appCfg)
	if configErr == nil && modeErr == nil && mode == GitHubAuthModeApp {
		return ctx
	}
	ctx = gitidentity.WithRuntime(ctx, s.gitRuntime)
	return gitidentity.WithResolver(ctx, func(operation context.Context) (gitidentity.Identity, error) {
		if configErr != nil {
			return gitidentity.Identity{}, configErr
		}
		if modeErr != nil {
			return gitidentity.Identity{}, modeErr
		}
		c, err := s.PersonalConnection(operation)
		if err != nil {
			return gitidentity.Identity{}, err
		}
		base, err := personalGitHubAPIEndpoint(c.APIEndpoint)
		if err != nil {
			return gitidentity.Identity{}, err
		}
		u, err := url.Parse(base)
		if err != nil {
			return gitidentity.Identity{}, err
		}
		host := u.Host
		if host == "api.github.com" {
			host = "github.com"
		}
		principal, _ := models.PrincipalFromContext(s.personalContext(operation))
		return gitidentity.Identity{Name: c.AuthorName, Email: c.AuthorEmail, Token: c.PAT, Host: host, Scope: principal.UserID}, nil
	})
}

func (s *GitHubService) SetGitRunIdentities(store persistence.RunIdentityStore) {
	s.gitRunIdentities = store
}
func (s *GitHubService) TaskGitContext(ctx context.Context, taskID, executionID string) (context.Context, error) {
	if _, ok := models.PrincipalFromContext(ctx); ok {
		return s.GitContext(ctx), nil
	}
	if s.gitRunIdentities == nil {
		return s.GitContext(ctx), nil
	}
	var user string
	var err error
	if executionID != "" {
		var found bool
		user, found, err = s.gitRunIdentities.ExecutionUser(ctx, executionID)
		if err != nil {
			return ctx, err
		}
		if found && user == "" {
			return ctx, persistence.ErrProjectDenied
		}
	}
	if user == "" {
		user, err = s.gitRunIdentities.TaskUser(ctx, taskID)
		if err != nil {
			return ctx, err
		}
	}
	if user == "" && s.credentialAuthOff {
		user = models.BootstrapUserID
	}
	if user == "" {
		return ctx, persistence.ErrProjectDenied
	}
	return s.GitContext(models.WithPrincipal(ctx, models.Principal{UserID: user})), nil
}

// UsesPersonalPAT distinguishes personal lifecycle actions from shared App actions.
func (s *GitHubService) UsesPersonalPAT(ctx context.Context) (bool, error) {
	if s.personalConnections == nil {
		return false, nil
	}
	cfg, err := s.getAppConfig(ctx)
	if err != nil {
		return false, err
	}
	mode, err := s.resolveAuthMode(ctx, cfg)
	return mode == GitHubAuthModePAT, err
}

func personalGitHubStatusProvider(s *GitHubService) automationGitHubConnectionProvider {
	if s == nil {
		return nil
	}
	return s
}
