package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/pkg/gitidentity"
	"github.com/stretchr/testify/require"
)

type personalConnectionTestStore struct {
	mu   sync.Mutex
	rows map[string]models.GitHubConnection
}

func (s *personalConnectionTestStore) GetOwn(ctx context.Context) (*models.GitHubConnection, error) {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return nil, persistence.ErrProjectDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.rows[p.UserID]
	return &c, nil
}
func (s *personalConnectionTestStore) SaveOwn(ctx context.Context, c models.GitHubConnection, revision int64) (bool, error) {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok || p.UserID == "" {
		return false, persistence.ErrProjectDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows[p.UserID].Revision != revision {
		return false, nil
	}
	c.Revision = revision + 1
	s.rows[p.UserID] = c
	return true, nil
}
func (s *personalConnectionTestStore) DeleteOwn(ctx context.Context, revision int64) (bool, error) {
	return s.SaveOwn(ctx, models.GitHubConnection{}, revision)
}
func personalServiceForTest() *GitHubService {
	s := NewGitHubService(nil, "", "", "", "")
	s.SetPersonalConnections(&personalConnectionTestStore{rows: map[string]models.GitHubConnection{
		"alice":                {PAT: "alice-token", AuthorName: "Alice", AuthorEmail: "alice@example.com", Revision: 1},
		"bob":                  {PAT: "bob-token", AuthorName: "Bob", AuthorEmail: "bob@example.com", Revision: 1},
		models.BootstrapUserID: {PAT: "owner-token", AuthorName: "Owner", AuthorEmail: "owner@example.com", Revision: 1},
	}})
	return s
}
func TestPersonalGitHubCredentialsUseActingUserWithoutFallback(t *testing.T) {
	s := personalServiceForTest()
	for _, user := range []string{"alice", "bob"} {
		t.Run(user, func(t *testing.T) {
			t.Parallel()
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: user})
			token, err := s.createOperationAccessToken(ctx, "https://api.github.com")
			require.NoError(t, err)
			require.Equal(t, user+"-token", token)
			_, err = s.createOperationAccessToken(ctx, "https://other.example/api/v3")
			require.Error(t, err)
			identity, err := gitidentity.Resolve(s.GitContext(ctx))
			require.NoError(t, err)
			require.Equal(t, user+"@example.com", identity.Email)
		})
	}
	_, err := s.createOperationAccessToken(t.Context(), "https://api.github.com")
	require.Error(t, err)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "new-member"})
	_, err = s.createOperationAccessToken(ctx, "https://api.github.com")
	require.ErrorContains(t, err, "not configured")
}
func TestPersonalGitHubAuthOffAndDisconnect(t *testing.T) {
	s := personalServiceForTest()
	s.credentialAuthOff = true
	token, err := s.createOperationAccessToken(t.Context(), "https://api.github.com")
	require.NoError(t, err)
	require.Equal(t, "owner-token", token)
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "alice"})
	require.NoError(t, s.Disconnect(ctx))
	_, err = s.createOperationAccessToken(ctx, "https://api.github.com")
	require.Error(t, err)
	token, err = s.createOperationAccessToken(t.Context(), "https://api.github.com")
	require.NoError(t, err)
	require.Equal(t, "owner-token", token)
}
func TestPersonalGitHubLookupEmailAndMissingEmailPermission(t *testing.T) {
	for _, emailAllowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "email", false: "manual"}[emailAllowed], func(t *testing.T) {
			s := personalServiceForTest()
			s.httpClient = &http.Client{Transport: githubRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "Bearer entered-token", r.Header.Get("Authorization"))
				body := `{"login":"alice-gh","name":"Alice GitHub"}`
				status := 200
				if r.URL.Path == "/user/emails" {
					body = `[{"email":"verified@example.com","primary":true,"verified":true}]`
					if !emailAllowed {
						status = 403
						body = `{}`
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "alice"})
			identity, err := s.LookupPersonalGitHubIdentity(ctx, "entered-token", "")
			require.NoError(t, err)
			require.Equal(t, "Alice GitHub", identity.AuthorName)
			if emailAllowed {
				require.Equal(t, "verified@example.com", identity.AuthorEmail)
			} else {
				require.Empty(t, identity.AuthorEmail)
			}
			saved, err := s.PersonalConnection(ctx)
			require.NoError(t, err)
			require.Equal(t, "alice-token", saved.PAT)
		})
	}
}

type gitIdentityRunStore struct {
	persistence.RunIdentityStore
	executionUser  string
	executionFound bool
}

func (s gitIdentityRunStore) TaskUser(context.Context, string) (string, error) { return "bob", nil }
func (s gitIdentityRunStore) ExecutionUser(context.Context, string) (string, bool, error) {
	return s.executionUser, s.executionFound, nil
}
func TestPersonalGitHubRestoresExecutionIdentityBeforeTaskIdentity(t *testing.T) {
	s := personalServiceForTest()
	s.SetGitRunIdentities(gitIdentityRunStore{executionUser: "alice", executionFound: true})
	ctx, err := s.TaskGitContext(t.Context(), "task", "execution")
	require.NoError(t, err)
	token, err := s.createOperationAccessToken(ctx, "https://api.github.com")
	require.NoError(t, err)
	require.Equal(t, "alice-token", token)
	ctx, err = s.TaskGitContext(t.Context(), "task", "")
	require.NoError(t, err)
	token, err = s.createOperationAccessToken(ctx, "https://api.github.com")
	require.NoError(t, err)
	require.Equal(t, "bob-token", token)
	s.SetGitRunIdentities(gitIdentityRunStore{executionFound: true})
	_, err = s.TaskGitContext(t.Context(), "task", "execution")
	require.ErrorIs(t, err, persistence.ErrProjectDenied)
}

func TestPersonalGitHubAuthOffLocalCommitDefaults(t *testing.T) {
	s := personalServiceForTest()
	s.personalConnections = &personalConnectionTestStore{rows: map[string]models.GitHubConnection{}}
	s.credentialAuthOff = true
	identity, err := gitidentity.Resolve(s.GitContext(t.Context()))
	require.NoError(t, err)
	require.Equal(t, "OpenVibely Bot", identity.Name)
	require.Equal(t, "bot@openvibely.ai", identity.Email)
	require.Empty(t, identity.Token)
	s.credentialAuthOff = false
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "alice"})
	require.Error(t, gitidentity.RequireCommit(s.GitContext(ctx)))
}
