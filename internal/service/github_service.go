package service

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openvibely/openvibely/internal/repository"
)

const (
	GitHubSettingAppID           = "github_app_id"
	GitHubSettingAppSlug         = "github_app_slug"
	GitHubSettingAppPrivateKey   = "github_app_private_key"
	GitHubSettingAuthMode        = "github_auth_mode"
	GitHubSettingPAT             = "github_pat"
	GitHubSettingPATUserLogin    = "github_pat_user_login"
	GitHubSettingProjectRepoRoot = "project_repo_root"

	GitHubAuthModePAT = "pat"
	GitHubAuthModeApp = "app"

	githubSettingInstallationID = "github_app_installation_id"
	githubSettingAccountLogin   = "github_app_account_login"
	githubSettingAccountType    = "github_app_account_type"
	githubSettingConnectedAt    = "github_app_connected_at"
	githubAPIAcceptHeaderValue  = "application/vnd.github+json"
	githubAPIVersionHeaderValue = "2022-11-28"
	defaultGitHubAPIBaseURL     = "https://api.github.com"
	defaultGitHubWebBaseURL     = "https://github.com"
)

type GitHubConnectionStatus struct {
	Configured     bool
	Connected      bool
	AuthMode       string
	InstallationID string
	AccountLogin   string
	AccountType    string
	HasPAT         bool
	AppConfigured  bool
}

type GitHubRepoRef struct {
	Owner    string
	Name     string
	FullName string
	CloneURL string
	HTMLURL  string
}

type GitHubPullRequest struct {
	Number int
	URL    string
	State  string
}

type GitHubCreatePullRequestRequest struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft bool
}

type runGitFunc func(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error)

type githubAppConfig struct {
	AppID           string
	AppSlug         string
	AppPrivateKey   string
	ProjectRepoRoot string
}

type GitHubService struct {
	settingsRepo    *repository.SettingsRepo
	appID           string
	appSlug         string
	appPrivateKey   string
	projectRepoRoot string
	httpClient      *http.Client
	apiBaseURL      string
	webBaseURL      string
	runGit          runGitFunc
	nowFn           func() time.Time
}

func NewGitHubService(settingsRepo *repository.SettingsRepo, appID, appSlug, appPrivateKey, projectRepoRoot string) *GitHubService {
	return &GitHubService{
		settingsRepo:    settingsRepo,
		appID:           strings.TrimSpace(appID),
		appSlug:         strings.TrimSpace(appSlug),
		appPrivateKey:   strings.TrimSpace(appPrivateKey),
		projectRepoRoot: strings.TrimSpace(projectRepoRoot),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
		apiBaseURL: defaultGitHubAPIBaseURL,
		webBaseURL: defaultGitHubWebBaseURL,
		runGit:     defaultRunGit,
		nowFn:      time.Now,
	}
}

func defaultRunGit(ctx context.Context, dir string, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Env = ensureGitSSLConfig(cmd.Env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return out, fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, msg)
		}
		return out, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// ensureGitSSLConfig ensures git has proper SSL/TLS certificate configuration
// by finding the system CA bundle or using GIT_SSL_CAINFO if already set.
func ensureGitSSLConfig(env []string) []string {
	// Check if SSL cert config is already provided
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSL_CAINFO=") || strings.HasPrefix(e, "SSL_CERT_FILE=") || strings.HasPrefix(e, "GIT_SSL_NO_VERIFY=") {
			return env // already configured
		}
	}

	// Try to find system CA bundle
	caBundlePaths := []string{
		"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu/Alpine
		"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL/CentOS
		"/etc/ssl/ca-bundle.pem",             // OpenSUSE
		"/etc/ssl/cert.pem",                  // OpenBSD (if it exists)
		"/usr/local/share/certs/ca-root-nss.crt", // FreeBSD
	}

	for _, path := range caBundlePaths {
		if _, err := os.Stat(path); err == nil {
			return append(env, "GIT_SSL_CAINFO="+path)
		}
	}

	// No CA bundle found - as a last resort, check if system git has a working config
	// by reading git config http.sslCAInfo
	cmd := exec.Command("git", "config", "--get", "http.sslCAInfo")
	if out, err := cmd.Output(); err == nil {
		if caPath := strings.TrimSpace(string(out)); caPath != "" {
			if _, err := os.Stat(caPath); err == nil {
				return append(env, "GIT_SSL_CAINFO="+caPath)
			}
		}
	}

	// If we still haven't found a CA bundle, check if GIT_SSL_NO_VERIFY is set in the process environment
	if os.Getenv("GIT_SSL_NO_VERIFY") != "" {
		return append(env, "GIT_SSL_NO_VERIFY="+os.Getenv("GIT_SSL_NO_VERIFY"))
	}
	
	// Last resort: disable SSL verification to prevent clone failures
	// This is not ideal for security but prevents the service from being unusable
	// Log a warning so admins know to configure proper certificates
	log.Println("WARNING: No valid SSL CA bundle found for Git HTTPS operations. Disabling SSL verification. Set GIT_SSL_CAINFO or GIT_SSL_NO_VERIFY environment variable to configure explicitly.")
	return append(env, "GIT_SSL_NO_VERIFY=true")
}

func (s *GitHubService) GetConnectionStatus(ctx context.Context) (GitHubConnectionStatus, error) {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return GitHubConnectionStatus{}, err
	}
	mode, err := s.resolveAuthMode(ctx, appCfg)
	if err != nil {
		return GitHubConnectionStatus{}, err
	}
	pat, err := s.getPAT(ctx)
	if err != nil {
		return GitHubConnectionStatus{}, err
	}

	status := GitHubConnectionStatus{
		AuthMode:      mode,
		HasPAT:        strings.TrimSpace(pat) != "",
		AppConfigured: appCfg.isConfigured(),
	}
	if mode == GitHubAuthModePAT {
		status.Configured = status.HasPAT
		status.Connected = status.HasPAT
		if s.settingsRepo != nil {
			patLogin, _ := s.settingsRepo.Get(ctx, GitHubSettingPATUserLogin)
			status.AccountLogin = strings.TrimSpace(patLogin)
			if status.AccountLogin != "" {
				status.AccountType = "User"
			}
		}
		return status, nil
	}
	status.Configured = appCfg.isConfigured()
	if s.settingsRepo == nil {
		return status, nil
	}

	installationID, err := s.settingsRepo.Get(ctx, githubSettingInstallationID)
	if err != nil {
		return status, err
	}
	accountLogin, _ := s.settingsRepo.Get(ctx, githubSettingAccountLogin)
	accountType, _ := s.settingsRepo.Get(ctx, githubSettingAccountType)

	status.InstallationID = installationID
	status.AccountLogin = accountLogin
	status.AccountType = accountType
	status.Connected = strings.TrimSpace(installationID) != ""
	return status, nil
}

func (s *GitHubService) ConnectURL(ctx context.Context) (string, error) {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return "", err
	}
	mode, err := s.resolveAuthMode(ctx, appCfg)
	if err != nil {
		return "", err
	}
	if mode != GitHubAuthModeApp {
		return "", fmt.Errorf("github app connect is available only in Advanced mode")
	}
	if !appCfg.isConfigured() {
		return "", fmt.Errorf("github app is not configured")
	}
	return fmt.Sprintf("https://github.com/apps/%s/installations/new", url.PathEscape(appCfg.AppSlug)), nil
}

func (s *GitHubService) HandleInstallCallback(ctx context.Context, installationID string) error {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return err
	}
	if !appCfg.isConfigured() {
		return fmt.Errorf("github app is not configured")
	}
	if s.settingsRepo == nil {
		return fmt.Errorf("settings repository not configured")
	}
	installationID = strings.TrimSpace(installationID)
	if installationID == "" {
		return fmt.Errorf("missing installation id")
	}
	if _, err := strconv.ParseInt(installationID, 10, 64); err != nil {
		return fmt.Errorf("invalid installation id")
	}

	if err := s.settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModeApp); err != nil {
		return err
	}
	if err := s.settingsRepo.Set(ctx, githubSettingInstallationID, installationID); err != nil {
		return err
	}
	if err := s.settingsRepo.Set(ctx, githubSettingConnectedAt, s.nowFn().UTC().Format(time.RFC3339)); err != nil {
		return err
	}

	accountLogin, accountType, err := s.fetchInstallationAccountMetadata(ctx, installationID)
	if err == nil {
		_ = s.settingsRepo.Set(ctx, githubSettingAccountLogin, accountLogin)
		_ = s.settingsRepo.Set(ctx, githubSettingAccountType, accountType)
	}
	return nil
}

func (s *GitHubService) Disconnect(ctx context.Context) error {
	if s.settingsRepo == nil {
		return nil
	}
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return err
	}
	mode, err := s.resolveAuthMode(ctx, appCfg)
	if err != nil {
		return err
	}
	if mode == GitHubAuthModePAT {
		if err := s.settingsRepo.Set(ctx, GitHubSettingPAT, ""); err != nil {
			return err
		}
		if err := s.settingsRepo.Set(ctx, GitHubSettingPATUserLogin, ""); err != nil {
			return err
		}
		return nil
	}
	if err := s.settingsRepo.Set(ctx, githubSettingInstallationID, ""); err != nil {
		return err
	}
	if err := s.settingsRepo.Set(ctx, githubSettingAccountLogin, ""); err != nil {
		return err
	}
	if err := s.settingsRepo.Set(ctx, githubSettingAccountType, ""); err != nil {
		return err
	}
	if err := s.settingsRepo.Set(ctx, githubSettingConnectedAt, ""); err != nil {
		return err
	}
	return nil
}

func (s *GitHubService) CloneProjectRepo(ctx context.Context, projectID, repoURL string) (string, string, error) {
	repo, err := ParseGitHubRepoURL(repoURL)
	if err != nil {
		return "", "", err
	}
	root, err := s.ensureRepoRoot(ctx)
	if err != nil {
		return "", "", err
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return "", "", err
	}

	dest := filepath.Join(root, projectID)
	if _, err := os.Stat(dest); err == nil {
		return "", "", fmt.Errorf("destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return "", "", err
	}

	if err := s.cloneWithToken(ctx, repo.CloneURL, dest, token); err != nil {
		return "", "", err
	}
	return dest, repo.HTMLURL, nil
}

func (s *GitHubService) RecloneProjectRepo(ctx context.Context, projectID, currentRepoPath, repoURL string) (string, string, error) {
	repo, err := ParseGitHubRepoURL(repoURL)
	if err != nil {
		return "", "", err
	}
	root, err := s.ensureRepoRoot(ctx)
	if err != nil {
		return "", "", err
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return "", "", err
	}

	tmpRoot := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmpRoot, 0755); err != nil {
		return "", "", err
	}
	tmpDest := filepath.Join(tmpRoot, fmt.Sprintf("%s-%d", projectID, s.nowFn().UnixNano()))
	if err := s.cloneWithToken(ctx, repo.CloneURL, tmpDest, token); err != nil {
		_ = os.RemoveAll(tmpDest)
		return "", "", err
	}

	dest := filepath.Join(root, projectID)
	backup := ""
	if _, err := os.Stat(dest); err == nil {
		backup = fmt.Sprintf("%s.bak.%d", dest, s.nowFn().UnixNano())
		if err := os.Rename(dest, backup); err != nil {
			_ = os.RemoveAll(tmpDest)
			return "", "", err
		}
	}
	if err := os.Rename(tmpDest, dest); err != nil {
		if backup != "" {
			_ = os.Rename(backup, dest)
		}
		_ = os.RemoveAll(tmpDest)
		return "", "", err
	}

	if backup != "" {
		_ = os.RemoveAll(backup)
	}

	if managed, _ := isPathWithin(root, currentRepoPath); managed {
		currentAbs, _ := filepath.Abs(currentRepoPath)
		destAbs, _ := filepath.Abs(dest)
		if currentAbs != "" && destAbs != "" && currentAbs != destAbs {
			_ = os.RemoveAll(currentAbs)
		}
	}

	return dest, repo.HTMLURL, nil
}

func (s *GitHubService) ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL != "" {
		repo, err := ParseGitHubRepoURL(repoURL)
		if err != nil {
			return nil, err
		}
		return &repo, nil
	}
	if strings.TrimSpace(repoPath) == "" {
		return nil, fmt.Errorf("project has no repository path")
	}

	out, err := s.runGit(ctx, repoPath, nil, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Errorf("reading origin remote: %w", err)
	}
	remoteURL := strings.TrimSpace(string(out))
	if remoteURL == "" {
		return nil, fmt.Errorf("project repository has no origin remote")
	}
	repo, err := ParseGitHubRepoURL(remoteURL)
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (s *GitHubService) PushBranch(ctx context.Context, repoPath, worktreePath, branch string, repo *GitHubRepoRef) error {
	if repo == nil {
		return fmt.Errorf("repository reference is required")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}

	dir := strings.TrimSpace(worktreePath)
	if dir == "" {
		dir = strings.TrimSpace(repoPath)
	}
	if dir == "" {
		return fmt.Errorf("repository path is required")
	}

	extraEnv := gitHubTokenEnv(token)
	_, err = s.runGit(ctx, dir, extraEnv, "push", "--set-upstream", repo.CloneURL, fmt.Sprintf("%s:%s", branch, branch))
	if err != nil {
		return fmt.Errorf("pushing branch: %w", err)
	}
	return nil
}

func (s *GitHubService) FindPullRequestByBranch(ctx context.Context, repo *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, fmt.Errorf("branch is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	head := url.QueryEscape(fmt.Sprintf("%s:%s", repo.Owner, branch))
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls?state=all&head=%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), head)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)

	var prs []struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		State  string `json:"state"`
	}
	if err := s.doGitHubJSON(req, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &GitHubPullRequest{Number: prs[0].Number, URL: prs[0].URL, State: prs[0].State}, nil
}

func (s *GitHubService) CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, createReq GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	if strings.TrimSpace(createReq.Title) == "" || strings.TrimSpace(createReq.Head) == "" || strings.TrimSpace(createReq.Base) == "" {
		return nil, fmt.Errorf("pull request title/head/base are required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"title": createReq.Title,
		"head":  createReq.Head,
		"base":  createReq.Base,
		"body":  createReq.Body,
		"draft": createReq.Draft,
	}
	body, _ := json.Marshal(payload)

	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	var created struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		State  string `json:"state"`
	}
	if err := s.doGitHubJSON(req, &created); err != nil {
		return nil, err
	}

	return &GitHubPullRequest{Number: created.Number, URL: created.URL, State: created.State}, nil
}

func ParseGitHubRepoURL(raw string) (GitHubRepoRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return GitHubRepoRef{}, fmt.Errorf("repository URL is required")
	}

	owner, repo := "", ""
	switch {
	case strings.HasPrefix(trimmed, "git@"):
		parts := strings.SplitN(strings.TrimPrefix(trimmed, "git@"), ":", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "github.com") {
			return GitHubRepoRef{}, fmt.Errorf("unsupported git remote host")
		}
		owner, repo = splitOwnerRepo(parts[1])
	case strings.HasPrefix(strings.ToLower(trimmed), "ssh://"):
		u, err := url.Parse(trimmed)
		if err != nil {
			return GitHubRepoRef{}, fmt.Errorf("invalid repository URL")
		}
		if !strings.EqualFold(u.Hostname(), "github.com") {
			return GitHubRepoRef{}, fmt.Errorf("unsupported repository host")
		}
		owner, repo = splitOwnerRepo(strings.TrimPrefix(u.Path, "/"))
	case strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://"):
		u, err := url.Parse(trimmed)
		if err != nil {
			return GitHubRepoRef{}, fmt.Errorf("invalid repository URL")
		}
		if !strings.EqualFold(u.Hostname(), "github.com") {
			return GitHubRepoRef{}, fmt.Errorf("unsupported repository host")
		}
		owner, repo = splitOwnerRepo(strings.TrimPrefix(u.Path, "/"))
	default:
		owner, repo = splitOwnerRepo(trimmed)
	}

	if owner == "" || repo == "" {
		return GitHubRepoRef{}, fmt.Errorf("invalid GitHub repository URL")
	}

	htmlURL := fmt.Sprintf("https://github.com/%s/%s", owner, repo)
	cloneURL := htmlURL + ".git"
	return GitHubRepoRef{
		Owner:    owner,
		Name:     repo,
		FullName: owner + "/" + repo,
		CloneURL: cloneURL,
		HTMLURL:  htmlURL,
	}, nil
}

func splitOwnerRepo(path string) (string, string) {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", ""
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(parts[1])
	repo = strings.TrimSuffix(repo, ".git")
	if owner == "" || repo == "" {
		return "", ""
	}
	return owner, repo
}

func (s *GitHubService) fetchInstallationAccountMetadata(ctx context.Context, installationID string) (string, string, error) {
	appJWT, err := s.generateAppJWT(ctx)
	if err != nil {
		return "", "", err
	}

	endpoint := fmt.Sprintf("%s/app/installations/%s", s.apiBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", err
	}
	s.applyGitHubHeaders(req, appJWT)

	var resp struct {
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	}
	if err := s.doGitHubJSON(req, &resp); err != nil {
		return "", "", err
	}
	return resp.Account.Login, resp.Account.Type, nil
}

func (s *GitHubService) createOperationAccessToken(ctx context.Context) (string, error) {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return "", err
	}
	mode, err := s.resolveAuthMode(ctx, appCfg)
	if err != nil {
		return "", err
	}
	if mode == GitHubAuthModePAT {
		pat, err := s.getPAT(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(pat) == "" {
			return "", fmt.Errorf("github personal access token is not configured")
		}
		return strings.TrimSpace(pat), nil
	}
	return s.createInstallationAccessToken(ctx)
}

func (s *GitHubService) createInstallationAccessToken(ctx context.Context) (string, error) {
	if s.settingsRepo == nil {
		return "", fmt.Errorf("settings repository not configured")
	}
	installationID, err := s.settingsRepo.Get(ctx, githubSettingInstallationID)
	if err != nil {
		return "", err
	}
	installationID = strings.TrimSpace(installationID)
	if installationID == "" {
		return "", fmt.Errorf("github is not connected")
	}

	appJWT, err := s.generateAppJWT(ctx)
	if err != nil {
		return "", err
	}

	endpoint := fmt.Sprintf("%s/app/installations/%s/access_tokens", s.apiBaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, appJWT)
	req.Header.Set("Content-Type", "application/json")

	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := s.doGitHubJSON(req, &tokenResp); err != nil {
		return "", err
	}
	if strings.TrimSpace(tokenResp.Token) == "" {
		return "", fmt.Errorf("received empty installation token")
	}
	return tokenResp.Token, nil
}

func (s *GitHubService) cloneWithToken(ctx context.Context, cloneURL, destPath, token string) error {
	extraEnv := gitHubTokenEnv(token)
	if _, err := s.runGit(ctx, "", extraEnv, "clone", cloneURL, destPath); err != nil {
		return fmt.Errorf("cloning repository: %w", err)
	}
	return nil
}

// GitAuthEnvForRepo returns git CLI environment variables needed to authenticate
// remote git operations for a GitHub-backed repo. Returns nil when repo/auth is unavailable.
func (s *GitHubService) GitAuthEnvForRepo(ctx context.Context, repoPath string) []string {
	if strings.TrimSpace(repoPath) == "" {
		return nil
	}
	if _, err := s.ResolveRepo(ctx, "", repoPath); err != nil {
		return nil
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		return nil
	}
	return gitHubTokenEnv(token)
}

func gitHubTokenEnv(token string) []string {
	auth := "x-access-token:" + token
	basicToken := base64.StdEncoding.EncodeToString([]byte(auth))
	extraHeader := "AUTHORIZATION: Basic " + basicToken
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=" + extraHeader,
	}
}

func (s *GitHubService) ensureRepoRoot(ctx context.Context) (string, error) {
	root := strings.TrimSpace(s.projectRepoRoot)
	if s.settingsRepo != nil {
		if settingRoot, err := s.settingsRepo.Get(ctx, GitHubSettingProjectRepoRoot); err == nil && strings.TrimSpace(settingRoot) != "" {
			root = strings.TrimSpace(settingRoot)
		}
	}
	if root == "" {
		root = "./repos"
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return "", err
	}
	return abs, nil
}

func isPathWithin(baseDir, candidate string) (bool, error) {
	if strings.TrimSpace(baseDir) == "" || strings.TrimSpace(candidate) == "" {
		return false, nil
	}
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return false, err
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false, err
	}
	if candidateAbs == baseAbs {
		return true, nil
	}
	prefix := baseAbs + string(os.PathSeparator)
	return strings.HasPrefix(candidateAbs, prefix), nil
}

func (s *GitHubService) applyGitHubHeaders(req *http.Request, bearerToken string) {
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Accept", githubAPIAcceptHeaderValue)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersionHeaderValue)
}

func (s *GitHubService) doGitHubJSON(req *http.Request, target any) error {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github API request failed (%d)", resp.StatusCode)
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func (s *GitHubService) generateAppJWT(ctx context.Context) (string, error) {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return "", err
	}
	if !appCfg.isConfigured() {
		return "", fmt.Errorf("github app is not configured")
	}

	privateKey, err := parseGitHubAppPrivateKey(appCfg.AppPrivateKey)
	if err != nil {
		return "", err
	}
	now := s.nowFn().UTC()
	claims := jwt.MapClaims{
		"iss": appCfg.AppID,
		"iat": now.Unix() - 60,
		"exp": now.Add(9 * time.Minute).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		return "", err
	}
	return signed, nil
}

func parseGitHubAppPrivateKey(raw string) (*rsa.PrivateKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("github app private key is empty")
	}
	normalized := strings.ReplaceAll(raw, `\\n`, "\n")
	block, _ := pem.Decode([]byte(normalized))
	if block == nil {
		return nil, fmt.Errorf("invalid github app private key")
	}
	if pkcs1, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return pkcs1, nil
	}
	pkcs8, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing github app private key: %w", err)
	}
	priv, ok := pkcs8.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("github app private key must be RSA")
	}
	return priv, nil
}

func (s *GitHubService) isConfigured() bool {
	return strings.TrimSpace(s.appID) != "" && strings.TrimSpace(s.appSlug) != "" && strings.TrimSpace(s.appPrivateKey) != ""
}

func (cfg githubAppConfig) isConfigured() bool {
	return strings.TrimSpace(cfg.AppID) != "" && strings.TrimSpace(cfg.AppSlug) != "" && strings.TrimSpace(cfg.AppPrivateKey) != ""
}

func (s *GitHubService) getAppConfig(ctx context.Context) (githubAppConfig, error) {
	cfg := githubAppConfig{
		AppID:           strings.TrimSpace(s.appID),
		AppSlug:         strings.TrimSpace(s.appSlug),
		AppPrivateKey:   strings.TrimSpace(s.appPrivateKey),
		ProjectRepoRoot: strings.TrimSpace(s.projectRepoRoot),
	}
	if s.settingsRepo == nil {
		return cfg, nil
	}

	if appID, err := s.settingsRepo.Get(ctx, GitHubSettingAppID); err == nil && strings.TrimSpace(appID) != "" {
		cfg.AppID = strings.TrimSpace(appID)
	} else if err != nil {
		return githubAppConfig{}, err
	}
	if appSlug, err := s.settingsRepo.Get(ctx, GitHubSettingAppSlug); err == nil && strings.TrimSpace(appSlug) != "" {
		cfg.AppSlug = strings.TrimSpace(appSlug)
	} else if err != nil {
		return githubAppConfig{}, err
	}
	if appPrivateKey, err := s.settingsRepo.Get(ctx, GitHubSettingAppPrivateKey); err == nil && strings.TrimSpace(appPrivateKey) != "" {
		cfg.AppPrivateKey = strings.TrimSpace(appPrivateKey)
	} else if err != nil {
		return githubAppConfig{}, err
	}
	if repoRoot, err := s.settingsRepo.Get(ctx, GitHubSettingProjectRepoRoot); err == nil && strings.TrimSpace(repoRoot) != "" {
		cfg.ProjectRepoRoot = strings.TrimSpace(repoRoot)
	} else if err != nil {
		return githubAppConfig{}, err
	}

	return cfg, nil
}

func NormalizeGitHubAuthMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case GitHubAuthModeApp:
		return GitHubAuthModeApp
	case GitHubAuthModePAT:
		return GitHubAuthModePAT
	default:
		return GitHubAuthModePAT
	}
}

func (s *GitHubService) resolveAuthMode(ctx context.Context, appCfg githubAppConfig) (string, error) {
	mode := ""
	if s.settingsRepo != nil {
		storedMode, err := s.settingsRepo.Get(ctx, GitHubSettingAuthMode)
		if err != nil {
			return "", err
		}
		mode = strings.ToLower(strings.TrimSpace(storedMode))
		if mode == GitHubAuthModePAT || mode == GitHubAuthModeApp {
			return mode, nil
		}

		pat, err := s.settingsRepo.Get(ctx, GitHubSettingPAT)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(pat) != "" {
			return GitHubAuthModePAT, nil
		}
	}
	if appCfg.isConfigured() {
		return GitHubAuthModeApp, nil
	}
	return GitHubAuthModePAT, nil
}

func (s *GitHubService) getPAT(ctx context.Context) (string, error) {
	if s.settingsRepo == nil {
		return "", nil
	}
	pat, err := s.settingsRepo.Get(ctx, GitHubSettingPAT)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(pat), nil
}
