package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerCurrentImage(t *testing.T) {
	for _, custom := range []string{"", "example/computer:custom"} {
		t.Run(custom, func(t *testing.T) {
			t.Setenv("OPENVIBELY_COMPUTER_IMAGE", custom)
			expected := custom
			if expected == "" {
				expected = computerImageTag()
			}
			for _, image := range []string{"old-image", expected} {
				p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
					require.Equal(t, []string{"inspect", computerContainerName("project")}, args)
					return json.Marshal([]any{map[string]any{"Config": map[string]any{"Image": image, "Labels": map[string]string{"openvibely.project": "project", "openvibely.computer-isolation": "1"}}}})
				}}
				require.Equal(t, image == expected, p.Current(t.Context(), "project"))
			}
		})
	}
}

type upgradeComputerFixture struct {
	startingComputerFixture
	current bool
}

func (p *upgradeComputerFixture) Current(context.Context, string) bool { return p.current }
func (p *upgradeComputerFixture) Start(ctx context.Context, id string) (ComputerConnection, error) {
	p.current = true
	return p.startingComputerFixture.Start(ctx, id)
}
func TestComputerAutomaticStartUpgradesHealthyOldImage(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Upgrade"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ready":true}`)) }))
	defer server.Close()
	provider := &upgradeComputerFixture{startingComputerFixture: startingComputerFixture{endpoint: server.URL, running: true}}
	svc := NewProjectComputerService(db.Projects, provider)
	require.NoError(t, db.Projects.Computers().Configure(t.Context(), project.ID, "local"))
	require.False(t, svc.Ready(t.Context(), project.ID))
	require.NoError(t, svc.EnsureStarted(t.Context(), project.ID))
	require.Equal(t, 1, provider.starts)
	require.True(t, svc.Ready(t.Context(), project.ID))
	require.NoError(t, svc.EnsureStarted(t.Context(), project.ID))
	require.Equal(t, 1, provider.starts)
}
