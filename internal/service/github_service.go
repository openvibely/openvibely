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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/repository"
	"golang.org/x/sync/errgroup"
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
	Number           int
	URL              string
	State            string
	HeadRef          string
	HeadRepoFullName string
}

type GitHubIssue struct {
	Number    int
	URL       string
	Title     string
	Body      string
	State     string
	UserLogin string
	Assignees []string
	Labels    []string
}

type GitHubIssueWithPullRequest struct {
	Issue       GitHubIssue
	PullRequest GitHubPullRequest
}

type GitHubPullRequestFeedback struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	AuthorLogin string    `json:"author_login"`
	AuthorType  string    `json:"author_type,omitempty"`
	Body        string    `json:"body"`
	URL         string    `json:"url"`
	State       string    `json:"state,omitempty"`
	Path        string    `json:"path,omitempty"`
	Line        int       `json:"line,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type GitHubAuthenticatedUser struct {
	Login  string `json:"login"`
	Source string `json:"source"`
}

const (
	GitHubLabelSuggestion     = "suggestion"
	GitHubLabelApproved       = "approved"
	GitHubLabelInProgress     = "in-progress"
	GitHubLabelTaskCreated    = "task-created"
	GitHubLabelPROpened       = "pr-opened"
	GitHubLabelBlocked        = "blocked"
	GitHubLabelNeedsHuman     = "needs-human"
	GitHubLabelDone           = "done"
	GitHubLabelDuplicate      = "duplicate"
	GitHubLabelBug            = "bug"
	GitHubLabelFeature        = "feature"
	GitHubLabelPerformance    = "performance"
	GitHubLabelDuplication    = "duplication"
	GitHubLabelSecurityReview = "security-review"
)

var DefaultGitHubSDLCLabels = []string{
	GitHubLabelSuggestion,
	GitHubLabelApproved,
	GitHubLabelInProgress,
	GitHubLabelTaskCreated,
	GitHubLabelPROpened,
	GitHubLabelBlocked,
	GitHubLabelNeedsHuman,
	GitHubLabelDone,
	GitHubLabelDuplicate,
	GitHubLabelBug,
	GitHubLabelFeature,
	GitHubLabelPerformance,
	GitHubLabelDuplication,
	GitHubLabelSecurityReview,
}

type GitHubCreatePullRequestRequest struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft bool
}

type GitHubPublishBranchRequest struct {
	RepoPath       string
	WorktreePath   string
	Branch         string
	BaseBranch     string
	CommitMessage  string
	CommitterName  string
	CommitterEmail string
}

type GitHubReplaceBranchHeadRequest struct {
	WorktreePath string
	Branch       string
	ExpectedHead string
}

type githubBranchChange struct {
	Path    string
	Content []byte
	Mode    string
	Delete  bool
}

type GitHubCreateIssueRequest struct {
	Title     string
	Body      string
	Labels    []string
	Assignees []string
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
		"/etc/ssl/certs/ca-certificates.crt",     // Debian/Ubuntu/Alpine
		"/etc/pki/tls/certs/ca-bundle.crt",       // RHEL/CentOS
		"/etc/ssl/ca-bundle.pem",                 // OpenSUSE
		"/etc/ssl/cert.pem",                      // OpenBSD (if it exists)
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
	applog.Infof("WARNING: No valid SSL CA bundle found for Git HTTPS operations. Disabling SSL verification. Set GIT_SSL_CAINFO or GIT_SSL_NO_VERIFY environment variable to configure explicitly.")
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

	dest := filepath.Join(root, projectID)
	if _, err := os.Stat(dest); err == nil {
		return "", "", fmt.Errorf("destination already exists: %s", dest)
	} else if !os.IsNotExist(err) {
		return "", "", err
	}

	if err := s.cloneProjectRepo(ctx, repo.CloneURL, localGitCloneURL(repoURL, repo.CloneURL), dest); err != nil {
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

	tmpRoot := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmpRoot, 0755); err != nil {
		return "", "", err
	}
	tmpDest := filepath.Join(tmpRoot, fmt.Sprintf("%s-%d", projectID, s.nowFn().UnixNano()))
	if err := s.cloneProjectRepo(ctx, repo.CloneURL, localGitCloneURL(repoURL, repo.CloneURL), tmpDest); err != nil {
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

	repo, remoteName, err := s.resolveRepoFromGitRemotes(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if repo.FullName == "" {
		return nil, fmt.Errorf("project repository remote %q is not a GitHub repository", remoteName)
	}
	return repo, nil
}

func (s *GitHubService) resolveRepoFromGitRemotes(ctx context.Context, repoPath string) (*GitHubRepoRef, string, error) {
	remotesOut, err := s.runGit(ctx, repoPath, nil, "remote")
	if err != nil {
		return nil, "", fmt.Errorf("listing git remotes: %w", err)
	}
	seen := map[string]bool{}
	remoteNames := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(remotesOut)), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		remoteNames = append(remoteNames, name)
	}
	if len(remoteNames) == 0 {
		return nil, "", fmt.Errorf("project repository has no git remotes")
	}

	preferred := []string{"origin", "upstream"}
	ordered := make([]string, 0, len(remoteNames)+len(preferred))
	for _, name := range preferred {
		if seen[name] {
			ordered = append(ordered, name)
		}
	}
	for _, name := range remoteNames {
		if name != "origin" && name != "upstream" {
			ordered = append(ordered, name)
		}
	}

	var lastErr error
	for _, name := range ordered {
		out, err := s.runGit(ctx, repoPath, nil, "remote", "get-url", name)
		if err != nil {
			lastErr = fmt.Errorf("reading %s remote: %w", name, err)
			continue
		}
		remoteURL := strings.TrimSpace(string(out))
		if remoteURL == "" {
			lastErr = fmt.Errorf("project repository remote %q has no URL", name)
			continue
		}
		repo, err := ParseGitHubRepoURL(remoteURL)
		if err != nil {
			lastErr = fmt.Errorf("project repository remote %q is not a GitHub repository: %w", name, err)
			continue
		}
		return &repo, name, nil
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("project repository has no GitHub remotes")
}

func (s *GitHubService) DefaultBranch(ctx context.Context, repo *GitHubRepoRef) (string, error) {
	if repo == nil {
		return "", fmt.Errorf("repository reference is required")
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return "", err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	var payload struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.doGitHubJSON(req, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.DefaultBranch) == "" {
		return "", fmt.Errorf("repository default branch is empty")
	}
	return strings.TrimSpace(payload.DefaultBranch), nil
}

func (s *GitHubService) ReplaceBranchHead(ctx context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error {
	if repo == nil {
		return fmt.Errorf("repository reference is required")
	}
	dir := strings.TrimSpace(req.WorktreePath)
	if dir == "" {
		return fmt.Errorf("worktree path is required")
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		return fmt.Errorf("branch is required")
	}
	expectedHead := strings.ToLower(strings.TrimSpace(req.ExpectedHead))
	if !isGitHubCommitSHA(expectedHead) {
		return fmt.Errorf("expected remote head must be a 40-character GitHub commit SHA")
	}
	if _, err := s.runGit(ctx, dir, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("invalid replacement branch %q: %w", branch, err)
	}
	currentBranch, err := s.runGit(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("resolving replacement worktree branch: %w", err)
	}
	if strings.TrimSpace(string(currentBranch)) != branch {
		return fmt.Errorf("worktree must be checked out on task branch %q before replacing pull request history", branch)
	}
	status, err := s.runGit(ctx, dir, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("checking replacement worktree: %w", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return fmt.Errorf("worktree must be clean before replacing pull request branch history")
	}
	if _, err := s.runGit(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return fmt.Errorf("resolving replacement worktree HEAD: %w", err)
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}
	remoteURL := fmt.Sprintf("%s/%s/%s.git", strings.TrimRight(s.webBaseURL, "/"), url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	lease := fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", branch, expectedHead)
	refspec := fmt.Sprintf("HEAD:refs/heads/%s", branch)
	if _, err := s.runGit(ctx, dir, gitHubTokenEnv(token), "push", lease, remoteURL, refspec); err != nil {
		return fmt.Errorf("lease-guarded branch replacement failed; remote head may have changed: %w", err)
	}
	return nil
}

func isGitHubCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (s *GitHubService) PublishBranch(ctx context.Context, repo *GitHubRepoRef, publishReq GitHubPublishBranchRequest) error {
	if repo == nil {
		return fmt.Errorf("repository reference is required")
	}
	branch := strings.TrimSpace(publishReq.Branch)
	if branch == "" {
		return fmt.Errorf("branch is required")
	}
	baseBranch := strings.TrimSpace(publishReq.BaseBranch)
	if baseBranch == "" {
		defaultBranch, err := s.DefaultBranch(ctx, repo)
		if err != nil {
			return fmt.Errorf("resolving default branch: %w", err)
		}
		baseBranch = defaultBranch
	}
	message := strings.TrimSpace(publishReq.CommitMessage)
	if message == "" {
		return fmt.Errorf("commit message is required")
	}

	dir := strings.TrimSpace(publishReq.WorktreePath)
	if dir == "" {
		dir = strings.TrimSpace(publishReq.RepoPath)
	}
	if dir == "" {
		return fmt.Errorf("repository path is required")
	}

	changes, err := collectGitHubBranchChanges(ctx, dir, baseBranch)
	if err != nil {
		return err
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}
	remoteBaseSHA, err := s.githubBranchCommitSHA(ctx, token, repo, baseBranch)
	if err != nil {
		return fmt.Errorf("resolving remote base branch %q: %w", baseBranch, err)
	}
	parentSHA := remoteBaseSHA
	remoteBranchSHA := ""
	if sha, err := s.githubBranchCommitSHA(ctx, token, repo, branch); err == nil {
		remoteBranchSHA = sha
		parentSHA = sha
	} else if !isGitHubRefMissingError(err) {
		return fmt.Errorf("resolving remote publish branch %q: %w", branch, err)
	}
	if len(changes) == 0 {
		if remoteBranchSHA != "" {
			return nil
		}
		if err := s.publishExistingLocalCommitWithToken(ctx, token, repo, branch, remoteBaseSHA, false); err != nil {
			if !isGitHubRefAlreadyExistsError(err) {
				return err
			}
			if _, refErr := s.githubBranchCommitSHA(ctx, token, repo, branch); refErr != nil {
				return fmt.Errorf("confirming concurrently created remote publish branch %q: %w", branch, refErr)
			}
		}
		return nil
	}
	baseTreeSHA, err := s.githubCommitTreeSHA(ctx, token, repo, remoteBaseSHA)
	if err != nil {
		return err
	}
	treeSHA, err := s.createGitHubTree(ctx, token, repo, baseTreeSHA, changes)
	if err != nil {
		return err
	}
	if remoteBranchSHA != "" {
		remoteBranchTreeSHA, treeErr := s.githubCommitTreeSHA(ctx, token, repo, remoteBranchSHA)
		if treeErr != nil {
			return fmt.Errorf("resolving remote publish branch tree %q: %w", branch, treeErr)
		}
		if remoteBranchTreeSHA == treeSHA {
			return nil
		}
	}
	commitSHA, err := s.createGitHubCommit(ctx, token, repo, message, treeSHA, parentSHA, publishReq.CommitterName, publishReq.CommitterEmail)
	if err != nil {
		return err
	}
	if err := s.publishExistingLocalCommitWithToken(ctx, token, repo, branch, commitSHA, false); err != nil {
		nonFastForward := isGitHubNonFastForwardError(err)
		if !nonFastForward && !isGitHubRefAlreadyExistsError(err) {
			return err
		}
		latestBranchSHA, refErr := s.githubBranchCommitSHA(ctx, token, repo, branch)
		if refErr != nil {
			return fmt.Errorf("refreshing remote publish branch %q after update rejection: %w", branch, refErr)
		}
		if nonFastForward && latestBranchSHA == parentSHA {
			return err
		}
		latestBranchTreeSHA, treeErr := s.githubCommitTreeSHA(ctx, token, repo, latestBranchSHA)
		if treeErr != nil {
			return fmt.Errorf("resolving refreshed remote publish branch tree %q: %w", branch, treeErr)
		}
		if latestBranchTreeSHA == treeSHA {
			return nil
		}
		retryCommitSHA, retryErr := s.createGitHubCommit(ctx, token, repo, message, treeSHA, latestBranchSHA, publishReq.CommitterName, publishReq.CommitterEmail)
		if retryErr != nil {
			return retryErr
		}
		return s.publishExistingLocalCommitWithToken(ctx, token, repo, branch, retryCommitSHA, false)
	}
	return nil
}

func (s *GitHubService) publishExistingLocalCommit(ctx context.Context, repo *GitHubRepoRef, branch, sha string, force bool) error {
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}
	return s.publishExistingLocalCommitWithToken(ctx, token, repo, branch, sha, force)
}

func (s *GitHubService) publishExistingLocalCommitWithToken(ctx context.Context, token string, repo *GitHubRepoRef, branch, sha string, force bool) error {
	branch = strings.TrimSpace(branch)
	sha = strings.TrimSpace(sha)
	if branch == "" || sha == "" {
		return fmt.Errorf("branch and sha are required")
	}
	payload := map[string]any{"sha": sha, "force": force}
	body, _ := json.Marshal(payload)
	updateEndpoint := fmt.Sprintf("%s/repos/%s/%s/git/refs/heads/%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), pathEscapeGitRef(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, updateEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	if err := s.doGitHubJSON(req, nil); err == nil {
		return nil
	} else if !isGitHubRefMissingError(err) {
		return err
	}

	createPayload := map[string]any{"ref": "refs/heads/" + branch, "sha": sha}
	createBody, _ := json.Marshal(createPayload)
	createEndpoint := fmt.Sprintf("%s/repos/%s/%s/git/refs", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createEndpoint, bytes.NewReader(createBody))
	if err != nil {
		return err
	}
	s.applyGitHubHeaders(createReq, token)
	createReq.Header.Set("Content-Type", "application/json")
	return s.doGitHubJSON(createReq, nil)
}

func (s *GitHubService) githubBranchCommitSHA(ctx context.Context, token string, repo *GitHubRepoRef, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/ref/heads/%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), pathEscapeGitRef(branch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	var payload struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := s.doGitHubJSON(req, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Object.SHA) == "" {
		return "", fmt.Errorf("branch commit sha is empty")
	}
	return strings.TrimSpace(payload.Object.SHA), nil
}

func (s *GitHubService) githubCommitTreeSHA(ctx context.Context, token string, repo *GitHubRepoRef, commitSHA string) (string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/commits/%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(commitSHA))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	var payload struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := s.doGitHubJSON(req, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.Tree.SHA) == "" {
		return "", fmt.Errorf("base commit tree sha is empty")
	}
	return strings.TrimSpace(payload.Tree.SHA), nil
}

// githubTreeBlobUploadConcurrency bounds how many independent blob uploads run
// in parallel during branch publication. It is intentionally conservative to
// avoid overwhelming the GitHub API while still overlapping independent uploads.
const githubTreeBlobUploadConcurrency = 4

func (s *GitHubService) createGitHubTree(ctx context.Context, token string, repo *GitHubRepoRef, baseTreeSHA string, changes []githubBranchChange) (string, error) {
	// Preserve deterministic original ordering of the tree entries. Each blob
	// SHA is written back into the entry at its original index, so parallel
	// uploads never reorder the tree or misattribute a SHA to the wrong path.
	tree := make([]map[string]any, len(changes))
	blobIndexes := make([]int, 0, len(changes))
	for i, change := range changes {
		entry := map[string]any{"path": change.Path, "mode": change.Mode, "type": "blob"}
		if change.Delete {
			entry["sha"] = nil
		} else {
			blobIndexes = append(blobIndexes, i)
		}
		tree[i] = entry
	}

	// Deletion-only changes never start blob workers.
	if len(blobIndexes) > 0 {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(githubTreeBlobUploadConcurrency)
		for _, idx := range blobIndexes {
			idx := idx
			change := changes[idx]
			group.Go(func() error {
				blobSHA, err := s.createGitHubBlob(groupCtx, token, repo, change.Content)
				if err != nil {
					return fmt.Errorf("creating blob for %s: %w", change.Path, err)
				}
				tree[idx]["sha"] = blobSHA
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return "", err
		}
	}

	payload := map[string]any{"base_tree": baseTreeSHA, "tree": tree}
	body, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/trees", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	var created struct {
		SHA string `json:"sha"`
	}
	if err := s.doGitHubJSON(req, &created); err != nil {
		return "", err
	}
	if strings.TrimSpace(created.SHA) == "" {
		return "", fmt.Errorf("created tree sha is empty")
	}
	return strings.TrimSpace(created.SHA), nil
}

func (s *GitHubService) createGitHubBlob(ctx context.Context, token string, repo *GitHubRepoRef, content []byte) (string, error) {
	payload := map[string]any{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64"}
	body, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/blobs", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	var created struct {
		SHA string `json:"sha"`
	}
	if err := s.doGitHubJSON(req, &created); err != nil {
		return "", err
	}
	if strings.TrimSpace(created.SHA) == "" {
		return "", fmt.Errorf("created blob sha is empty")
	}
	return strings.TrimSpace(created.SHA), nil
}

func (s *GitHubService) createGitHubCommit(ctx context.Context, token string, repo *GitHubRepoRef, message, treeSHA, parentSHA, committerName, committerEmail string) (string, error) {
	payload := map[string]any{"message": message, "tree": treeSHA, "parents": []string{parentSHA}}
	if strings.TrimSpace(committerName) != "" && strings.TrimSpace(committerEmail) != "" {
		payload["committer"] = map[string]string{"name": strings.TrimSpace(committerName), "email": strings.TrimSpace(committerEmail)}
	}
	body, _ := json.Marshal(payload)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/commits", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	var created struct {
		SHA string `json:"sha"`
	}
	if err := s.doGitHubJSON(req, &created); err != nil {
		return "", err
	}
	if strings.TrimSpace(created.SHA) == "" {
		return "", fmt.Errorf("created commit sha is empty")
	}
	return strings.TrimSpace(created.SHA), nil
}

func isGitHubRefMissingError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "github api request failed (404)") || strings.Contains(msg, "reference does not exist")
}

func isGitHubRefAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "github api request failed (422)") && strings.Contains(msg, "reference already exists")
}

func isGitHubNonFastForwardError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "github api request failed (422)") && strings.Contains(msg, "not a fast forward")
}

func pathEscapeGitRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func localCommitSHA(ctx context.Context, dir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref+"^{commit}")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("git rev-parse %s returned empty sha", ref)
	}
	return sha, nil
}

func collectGitHubBranchChanges(ctx context.Context, dir, baseBranch string) ([]githubBranchChange, error) {
	tracked, err := collectTrackedGitHubBranchChanges(ctx, dir, baseBranch)
	if err != nil {
		return nil, err
	}
	untracked, err := collectUntrackedGitHubBranchChanges(ctx, dir)
	if err != nil {
		return nil, err
	}
	changesByPath := make(map[string]githubBranchChange, len(tracked)+len(untracked))
	for _, change := range tracked {
		changesByPath[change.Path] = change
	}
	for _, change := range untracked {
		changesByPath[change.Path] = change
	}
	paths := make([]string, 0, len(changesByPath))
	for path := range changesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]githubBranchChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, changesByPath[path])
	}
	return changes, nil
}

func collectTrackedGitHubBranchChanges(ctx context.Context, dir, baseBranch string) ([]githubBranchChange, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--name-status", "-z", baseBranch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("checking changed files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Split(string(out), "\x00")
	changes := make([]githubBranchChange, 0)
	for i := 0; i < len(fields); {
		status := strings.TrimSpace(fields[i])
		i++
		if status == "" {
			continue
		}
		code := status[:1]
		var path string
		if code == "R" || code == "C" {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("malformed git diff name-status output")
			}
			oldPath := cleanGitHubTreePath(fields[i])
			i++
			path = cleanGitHubTreePath(fields[i])
			i++
			if code == "R" && oldPath != "" && oldPath != path {
				changes = append(changes, githubBranchChange{Path: oldPath, Mode: "100644", Delete: true})
			}
		} else {
			if i >= len(fields) {
				return nil, fmt.Errorf("malformed git diff name-status output")
			}
			path = cleanGitHubTreePath(fields[i])
			i++
		}
		if path == "" {
			continue
		}
		if code == "D" {
			changes = append(changes, githubBranchChange{Path: path, Mode: "100644", Delete: true})
			continue
		}
		content, mode, err := readGitHubTreeFileContent(ctx, dir, path)
		if err != nil {
			return nil, err
		}
		changes = append(changes, githubBranchChange{Path: path, Content: content, Mode: mode})
	}
	return changes, nil
}

func collectUntrackedGitHubBranchChanges(ctx context.Context, dir string) ([]githubBranchChange, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("checking untracked files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Split(string(out), "\x00")
	changes := make([]githubBranchChange, 0, len(fields))
	for _, field := range fields {
		path := cleanGitHubTreePath(field)
		if path == "" {
			continue
		}
		absPath := filepath.Join(dir, filepath.FromSlash(path))
		info, err := os.Lstat(absPath)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		content, mode, err := readGitHubTreeFileContent(ctx, dir, path)
		if err != nil {
			return nil, err
		}
		changes = append(changes, githubBranchChange{Path: path, Content: content, Mode: mode})
	}
	return changes, nil
}

func readGitHubTreeFileContent(ctx context.Context, dir, relPath string) ([]byte, string, error) {
	mode := gitHubTreeMode(ctx, dir, relPath)
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	if mode == "120000" {
		target, err := os.Readlink(absPath)
		if err != nil {
			return nil, "", fmt.Errorf("reading symlink %s: %w", relPath, err)
		}
		return []byte(target), mode, nil
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading changed file %s: %w", relPath, err)
	}
	return content, mode, nil
}

func cleanGitHubTreePath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	path = strings.TrimPrefix(path, "./")
	if path == "." || path == "" || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") || strings.HasPrefix(path, "/") {
		return ""
	}
	return path
}

func gitHubTreeMode(ctx context.Context, dir, relPath string) string {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-s", "--", relPath)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err == nil {
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			switch fields[0] {
			case "100755", "120000":
				return fields[0]
			}
		}
	}
	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	if info, err := os.Lstat(absPath); err == nil {
		if info.Mode()&0111 != 0 {
			return "100755"
		}
	}
	return "100644"
}

func (s *GitHubService) GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	if number <= 0 {
		return nil, fmt.Errorf("pull request number must be positive")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)

	var pr struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
		State  string `json:"state"`
		Head   struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := s.doGitHubJSON(req, &pr); err != nil {
		return nil, err
	}
	return &GitHubPullRequest{Number: pr.Number, URL: pr.URL, State: pr.State, HeadRef: pr.Head.Ref, HeadRepoFullName: pr.Head.Repo.FullName}, nil
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

func (s *GitHubService) CreateIssue(ctx context.Context, repo *GitHubRepoRef, createReq GitHubCreateIssueRequest) (*GitHubIssue, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	createReq.Title = strings.TrimSpace(createReq.Title)
	if createReq.Title == "" {
		return nil, fmt.Errorf("issue title is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{"title": createReq.Title}
	if strings.TrimSpace(createReq.Body) != "" {
		payload["body"] = createReq.Body
	}
	labels, err := cleanGitHubIssueLabels(createReq.Labels)
	if err != nil {
		return nil, err
	}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	if assignees := cleanGitHubStringList(createReq.Assignees); len(assignees) > 0 {
		payload["assignees"] = assignees
	}
	body, _ := json.Marshal(payload)

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	var raw githubIssueAPI
	if err := s.doGitHubJSON(req, &raw); err != nil {
		return nil, err
	}
	issue := raw.toIssue()
	return &issue, nil
}

func (s *GitHubService) GetIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubIssue, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	if issueNumber <= 0 {
		return nil, fmt.Errorf("issue number is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), issueNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)

	var raw githubIssueAPI
	if err := s.doGitHubJSON(req, &raw); err != nil {
		return nil, err
	}
	issue := raw.toIssue()
	return &issue, nil
}

func (s *GitHubService) CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, bodyText string) error {
	if repo == nil {
		return fmt.Errorf("repository reference is required")
	}
	if issueNumber <= 0 {
		return fmt.Errorf("issue number is required")
	}
	bodyText = strings.TrimSpace(bodyText)
	if bodyText == "" {
		return fmt.Errorf("comment body is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]string{"body": bodyText})
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), issueNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	return s.doGitHubJSON(req, nil)
}

func (s *GitHubService) ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	if prNumber <= 0 {
		return nil, fmt.Errorf("pull request number is required")
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	type sourceResult struct {
		index    int
		feedback []GitHubPullRequestFeedback
		err      error
	}
	feedbackCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan sourceResult, 3)
	sources := []func(context.Context) ([]GitHubPullRequestFeedback, error){
		func(ctx context.Context) ([]GitHubPullRequestFeedback, error) {
			return s.listPullRequestIssueComments(ctx, token, repo, prNumber)
		},
		func(ctx context.Context) ([]GitHubPullRequestFeedback, error) {
			return s.listPullRequestReviews(ctx, token, repo, prNumber)
		},
		func(ctx context.Context) ([]GitHubPullRequestFeedback, error) {
			return s.listPullRequestReviewComments(ctx, token, repo, prNumber)
		},
	}
	for index, source := range sources {
		go func() {
			feedback, err := source(feedbackCtx)
			results <- sourceResult{index: index, feedback: feedback, err: err}
		}()
	}

	feedbackBySource := make([][]GitHubPullRequestFeedback, len(sources))
	for range sources {
		result := <-results
		if result.err != nil {
			cancel()
			return nil, result.err
		}
		feedbackBySource[result.index] = result.feedback
	}

	var feedback []GitHubPullRequestFeedback
	for _, sourceFeedback := range feedbackBySource {
		feedback = append(feedback, sourceFeedback...)
	}
	sort.SliceStable(feedback, func(i, j int) bool {
		return feedback[i].CreatedAt.Before(feedback[j].CreatedAt)
	})
	return feedback, nil
}

func (s *GitHubService) listPullRequestIssueComments(ctx context.Context, token string, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments?per_page=100", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), prNumber)
	raw, err := getPaginatedGitHubJSON[githubIssueCommentAPI](ctx, s, token, endpoint, "")
	if err != nil {
		return nil, err
	}
	out := make([]GitHubPullRequestFeedback, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.toFeedback())
	}
	return out, nil
}

func (s *GitHubService) listPullRequestReviews(ctx context.Context, token string, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews?per_page=100", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), prNumber)
	raw, err := getPaginatedGitHubJSON[githubPullRequestReviewAPI](ctx, s, token, endpoint, "")
	if err != nil {
		return nil, err
	}
	out := make([]GitHubPullRequestFeedback, 0, len(raw))
	for _, item := range raw {
		if fb := item.toFeedback(); strings.TrimSpace(fb.Body) != "" || strings.TrimSpace(fb.State) != "" {
			out = append(out, fb)
		}
	}
	return out, nil
}

func (s *GitHubService) listPullRequestReviewComments(ctx context.Context, token string, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/comments?per_page=100", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), prNumber)
	raw, err := getPaginatedGitHubJSON[githubPullRequestReviewCommentAPI](ctx, s, token, endpoint, "")
	if err != nil {
		return nil, err
	}
	out := make([]GitHubPullRequestFeedback, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.toFeedback())
	}
	return out, nil
}

func (s *GitHubService) AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error {
	if repo == nil {
		return fmt.Errorf("repository reference is required")
	}
	if issueNumber <= 0 {
		return fmt.Errorf("issue number is required")
	}
	cleaned, err := cleanGitHubIssueLabels(labels)
	if err != nil {
		return err
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("at least one label is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string][]string{"labels": cleaned})
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/labels", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), issueNumber)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	s.applyGitHubHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	return s.doGitHubJSON(req, nil)
}

func (s *GitHubService) GetAuthenticatedUser(ctx context.Context) (*GitHubAuthenticatedUser, error) {
	appCfg, err := s.getAppConfig(ctx)
	if err != nil {
		return nil, err
	}
	mode, err := s.resolveAuthMode(ctx, appCfg)
	if err != nil {
		return nil, err
	}
	if mode == GitHubAuthModeApp {
		if s.settingsRepo == nil {
			return nil, fmt.Errorf("settings repository not configured")
		}
		login, err := s.settingsRepo.Get(ctx, githubSettingAccountLogin)
		if err != nil {
			return nil, err
		}
		login = strings.TrimSpace(login)
		if login == "" {
			installationID, err := s.settingsRepo.Get(ctx, githubSettingInstallationID)
			if err != nil {
				return nil, err
			}
			login, accountType, err := s.fetchInstallationAccountMetadata(ctx, strings.TrimSpace(installationID))
			if err != nil {
				return nil, err
			}
			login = strings.TrimSpace(login)
			_ = s.settingsRepo.Set(ctx, githubSettingAccountLogin, login)
			_ = s.settingsRepo.Set(ctx, githubSettingAccountType, strings.TrimSpace(accountType))
		}
		if login == "" {
			return nil, fmt.Errorf("github app account login is unavailable")
		}
		return &GitHubAuthenticatedUser{Login: login, Source: GitHubAuthModeApp}, nil
	}

	if s.settingsRepo != nil {
		stored, err := s.settingsRepo.Get(ctx, GitHubSettingPATUserLogin)
		if err != nil {
			return nil, err
		}
		if login := strings.TrimSpace(stored); login != "" {
			return &GitHubAuthenticatedUser{Login: login, Source: GitHubAuthModePAT}, nil
		}
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/user", s.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	s.applyGitHubHeaders(req, token)

	var resp struct {
		Login string `json:"login"`
	}
	if err := s.doGitHubJSON(req, &resp); err != nil {
		return nil, err
	}
	login := strings.TrimSpace(resp.Login)
	if login == "" {
		return nil, fmt.Errorf("github authenticated user login is unavailable")
	}
	if s.settingsRepo != nil {
		_ = s.settingsRepo.Set(ctx, GitHubSettingPATUserLogin, login)
	}
	return &GitHubAuthenticatedUser{Login: login, Source: GitHubAuthModePAT}, nil
}

func (s *GitHubService) ListAssignedIssues(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return nil, fmt.Errorf("assignee is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	return s.listAssignedIssuesWithToken(ctx, repo, assignee, token)
}

func (s *GitHubService) ListAuthenticatedAssignedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
	user, err := s.GetAuthenticatedUser(ctx)
	if err != nil {
		return nil, nil, err
	}
	if user.Source == GitHubAuthModeApp {
		return nil, nil, fmt.Errorf("github_list_my_assigned_issues requires a PAT user token; GitHub App installations can be installed on organizations and do not identify an assignable issue user. Add the real issue assignee account to GitHub Authorized Users, call github_get_project_inbox, then call github_list_assigned_issues with each returned assignee")
	}
	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, nil, err
	}
	issues, err := s.listAssignedIssuesWithToken(ctx, repo, user.Login, token)
	if err != nil {
		return nil, nil, err
	}
	return user, issues, nil
}

func (s *GitHubService) listAssignedIssuesWithToken(ctx context.Context, repo *GitHubRepoRef, assignee, token string) ([]GitHubIssue, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	assignee = strings.TrimSpace(assignee)
	if assignee == "" {
		return nil, fmt.Errorf("assignee is required")
	}

	query := url.Values{}
	query.Set("state", "open")
	query.Set("assignee", assignee)
	query.Set("per_page", "100")
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues?%s", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), query.Encode())
	raw, err := getPaginatedGitHubJSON[githubIssueAPI](ctx, s, token, endpoint, "")
	if err != nil {
		return nil, err
	}

	issues := make([]GitHubIssue, 0, len(raw))
	for _, item := range raw {
		if item.PullRequest != nil {
			continue
		}
		issues = append(issues, item.toIssue())
	}
	return issues, nil
}

func (s *GitHubService) FindPullRequestForIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubPullRequest, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	if issueNumber <= 0 {
		return nil, fmt.Errorf("issue number is required")
	}

	token, err := s.createOperationAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/timeline?per_page=100", s.apiBaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), issueNumber)
	events, err := getPaginatedGitHubJSON[githubIssueTimelineEventAPI](ctx, s, token, endpoint, "application/vnd.github.mockingbird-preview+json")
	if err != nil {
		return nil, err
	}

	for _, event := range events {
		if event.Source.Issue.PullRequest == nil || event.Source.Issue.Number <= 0 {
			continue
		}
		return &GitHubPullRequest{
			Number: event.Source.Issue.Number,
			URL:    strings.TrimSpace(event.Source.Issue.HTMLURL),
			State:  strings.TrimSpace(event.Source.Issue.State),
		}, nil
	}
	return nil, nil
}

func (s *GitHubService) ListAssignedIssuesWithPullRequests(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssueWithPullRequest, error) {
	issues, err := s.ListAssignedIssues(ctx, repo, assignee)
	if err != nil {
		return nil, err
	}
	items := make([]GitHubIssueWithPullRequest, 0, len(issues))
	for _, issue := range issues {
		pr, err := s.FindPullRequestForIssue(ctx, repo, issue.Number)
		if err != nil {
			return nil, err
		}
		if pr == nil {
			continue
		}
		items = append(items, GitHubIssueWithPullRequest{Issue: issue, PullRequest: *pr})
	}
	return items, nil
}

func cleanGitHubIssueLabels(items []string) ([]string, error) {
	labels := cleanGitHubStringList(items)
	for _, label := range labels {
		if strings.HasPrefix(strings.ToLower(label), "openvibely:") {
			return nil, fmt.Errorf("GitHub issue labels must not use the openvibely: prefix: %s", label)
		}
	}
	return labels, nil
}

func cleanGitHubStringList(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

type githubIssueCommentAPI struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id"`
	URL       string    `json:"html_url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

func (c githubIssueCommentAPI) toFeedback() GitHubPullRequestFeedback {
	return GitHubPullRequestFeedback{
		Kind:        "issue_comment",
		ID:          strconv.FormatInt(c.ID, 10),
		NodeID:      strings.TrimSpace(c.NodeID),
		AuthorLogin: strings.TrimSpace(c.User.Login),
		AuthorType:  strings.TrimSpace(c.User.Type),
		Body:        c.Body,
		URL:         strings.TrimSpace(c.URL),
		CreatedAt:   c.CreatedAt,
	}
}

type githubPullRequestReviewAPI struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"`
	URL         string    `json:"html_url"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	User        struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

func (r githubPullRequestReviewAPI) toFeedback() GitHubPullRequestFeedback {
	return GitHubPullRequestFeedback{
		Kind:        "review",
		ID:          strconv.FormatInt(r.ID, 10),
		NodeID:      strings.TrimSpace(r.NodeID),
		AuthorLogin: strings.TrimSpace(r.User.Login),
		AuthorType:  strings.TrimSpace(r.User.Type),
		Body:        r.Body, URL: strings.TrimSpace(r.URL),
		State:     strings.TrimSpace(r.State),
		CreatedAt: r.SubmittedAt,
	}
}

type githubPullRequestReviewCommentAPI struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"node_id"`
	URL       string    `json:"html_url"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

func (c githubPullRequestReviewCommentAPI) toFeedback() GitHubPullRequestFeedback {
	return GitHubPullRequestFeedback{
		Kind:        "review_comment",
		ID:          strconv.FormatInt(c.ID, 10),
		NodeID:      strings.TrimSpace(c.NodeID),
		AuthorLogin: strings.TrimSpace(c.User.Login),
		AuthorType:  strings.TrimSpace(c.User.Type),
		Body:        c.Body,
		URL:         strings.TrimSpace(c.URL),
		Path:        strings.TrimSpace(c.Path), Line: c.Line,
		CreatedAt: c.CreatedAt,
	}
}

type githubIssueAPI struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct{} `json:"pull_request"`
}

func (i githubIssueAPI) toIssue() GitHubIssue {
	issue := GitHubIssue{
		Number:    i.Number,
		URL:       strings.TrimSpace(i.URL),
		Title:     strings.TrimSpace(i.Title),
		Body:      i.Body,
		State:     strings.TrimSpace(i.State),
		UserLogin: strings.TrimSpace(i.User.Login),
		Assignees: make([]string, 0, len(i.Assignees)),
		Labels:    make([]string, 0, len(i.Labels)),
	}
	for _, assignee := range i.Assignees {
		if login := strings.TrimSpace(assignee.Login); login != "" {
			issue.Assignees = append(issue.Assignees, login)
		}
	}
	for _, label := range i.Labels {
		if name := strings.TrimSpace(label.Name); name != "" {
			issue.Labels = append(issue.Labels, name)
		}
	}
	return issue
}

type githubIssueTimelineEventAPI struct {
	Source struct {
		Issue struct {
			Number      int       `json:"number"`
			HTMLURL     string    `json:"html_url"`
			State       string    `json:"state"`
			PullRequest *struct{} `json:"pull_request"`
		} `json:"issue"`
	} `json:"source"`
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

func (s *GitHubService) cloneProjectRepo(ctx context.Context, tokenCloneURL, localCloneURL, destPath string) error {
	token, tokenErr := s.createOperationAccessToken(ctx)
	if tokenErr == nil {
		return s.cloneWithToken(ctx, tokenCloneURL, destPath, token)
	}

	if err := s.cloneWithLocalGit(ctx, localCloneURL, destPath); err != nil {
		if shouldIncludeGitHubAuthUnavailableContext(err) {
			return fmt.Errorf("cloning repository with local git CLI fallback after GitHub auth was unavailable (%v): %w", tokenErr, err)
		}
		return err
	}
	return nil
}

func (s *GitHubService) cloneWithToken(ctx context.Context, cloneURL, destPath, token string) error {
	extraEnv := gitHubTokenEnv(token)
	if _, err := s.runGit(ctx, "", extraEnv, "clone", cloneURL, destPath); err != nil {
		return fmt.Errorf("cloning repository: %w", err)
	}
	return nil
}

func (s *GitHubService) cloneWithLocalGit(ctx context.Context, cloneURL, destPath string) error {
	if _, err := s.runGit(ctx, "", localGitCloneEnv(), "clone", cloneURL, destPath); err != nil {
		return fmt.Errorf("local git clone failed: %w", err)
	}
	return nil
}

func localGitCloneEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true",
		"SSH_ASKPASS=true",
	}
}

func localGitCloneURL(rawRepoURL, fallback string) string {
	trimmed := strings.TrimSpace(rawRepoURL)
	if trimmed == "" {
		return fallback
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "git@") || strings.HasPrefix(lower, "ssh://") || strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return trimmed
	}
	return fallback
}

func shouldIncludeGitHubAuthUnavailableContext(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	credentialFragments := []string{
		"authentication",
		"authenticate",
		"authorization",
		"authorized",
		"permission denied",
		"access denied",
		"could not read username",
		"terminal prompts disabled",
		"repository not found",
		"not found",
		"executable file not found",
		"command not found",
		"no such file or directory",
	}
	for _, fragment := range credentialFragments {
		if strings.Contains(msg, fragment) {
			return true
		}
	}
	return false
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

func getPaginatedGitHubJSON[T any](ctx context.Context, s *GitHubService, bearerToken, endpoint, accept string) ([]T, error) {
	var items []T
	seen := make(map[string]struct{})
	for next := endpoint; next != ""; {
		if _, ok := seen[next]; ok {
			return nil, fmt.Errorf("GitHub pagination Link cycle detected")
		}
		seen[next] = struct{}{}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return nil, err
		}
		s.applyGitHubHeaders(req, bearerToken)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			detail := formatGitHubAPIError(body)
			if detail != "" {
				return nil, fmt.Errorf("github API request failed (%d): %s", resp.StatusCode, detail)
			}
			return nil, fmt.Errorf("github API request failed (%d)", resp.StatusCode)
		}

		var page []T
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		items = append(items, page...)
		next, err = nextGitHubPageURL(resp.Header.Get("Link"), req.URL)
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

func nextGitHubPageURL(linkHeader string, base *url.URL) (string, error) {
	for _, link := range strings.Split(linkHeader, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		isNext := false
		for _, param := range parts[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(name, "rel") {
				for _, relation := range strings.Fields(strings.Trim(value, `"`)) {
					if relation == "next" {
						isNext = true
						break
					}
				}
			}
		}
		if !isNext {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if len(target) < 2 || target[0] != '<' || target[len(target)-1] != '>' {
			return "", fmt.Errorf("invalid GitHub pagination Link header")
		}
		next, err := url.Parse(target[1 : len(target)-1])
		if err != nil {
			return "", fmt.Errorf("invalid GitHub pagination Link header: %w", err)
		}
		resolved := base.ResolveReference(next)
		if !strings.EqualFold(resolved.Scheme, base.Scheme) || !strings.EqualFold(resolved.Host, base.Host) {
			return "", fmt.Errorf("GitHub pagination Link points to a different origin")
		}
		return resolved.String(), nil
	}
	return "", nil
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
		detail := formatGitHubAPIError(body)
		if detail != "" {
			return fmt.Errorf("github API request failed (%d): %s", resp.StatusCode, detail)
		}
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

func formatGitHubAPIError(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}

	var payload struct {
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
		Errors           []struct {
			Message  string `json:"message"`
			Resource string `json:"resource"`
			Field    string `json:"field"`
			Code     string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		parts := make([]string, 0, 1+len(payload.Errors))
		if msg := strings.TrimSpace(payload.Message); msg != "" {
			parts = append(parts, msg)
		}
		for _, item := range payload.Errors {
			detail := strings.TrimSpace(item.Message)
			if detail == "" {
				bits := make([]string, 0, 3)
				if resource := strings.TrimSpace(item.Resource); resource != "" {
					bits = append(bits, resource)
				}
				if field := strings.TrimSpace(item.Field); field != "" {
					bits = append(bits, field)
				}
				if code := strings.TrimSpace(item.Code); code != "" {
					bits = append(bits, code)
				}
				detail = strings.TrimSpace(strings.Join(bits, " "))
			}
			if detail != "" {
				parts = append(parts, detail)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
		if doc := strings.TrimSpace(payload.DocumentationURL); doc != "" {
			return doc
		}
	}

	if len(trimmed) > 300 {
		return trimmed[:300] + "..."
	}
	return trimmed
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
