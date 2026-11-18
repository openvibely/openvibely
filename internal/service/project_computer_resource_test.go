package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputerReadinessReconcilesProcessLimit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		limit        int
		current      bool
		owner, image string
		fail         bool
		updates      int
		wantError    bool
	}{
		{"upgrade", 1024, true, "project", "fixture", false, 1, false},
		{"already current", 4096, true, "project", "fixture", false, 0, false},
		{"cleanup does not update", 1024, false, "project", "fixture", false, 0, false},
		{"wrong owner", 1024, true, "other", "fixture", false, 0, true},
		{"outdated image", 1024, true, "project", "old", false, 0, true},
		{"update failure", 1024, true, "project", "fixture", true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "fixture")
			updates := 0
			limit := tc.limit
			failure := errors.New("update refused")
			p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
				switch args[0] {
				case "inspect":
					require.Equal(t, []string{"inspect", computerContainerName("project")}, args)
					return json.Marshal([]any{map[string]any{
						"Config":          map[string]any{"Image": tc.image, "Labels": map[string]string{"openvibely.project": tc.owner, "openvibely.computer-isolation": "1"}, "Env": []string{"OPENVIBELY_COMPUTER_TOKEN=fixture"}},
						"State":           map[string]bool{"Running": true},
						"HostConfig":      map[string]int{"PidsLimit": limit},
						"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": "1234"}}}},
					}})
				case "update":
					require.Equal(t, []string{"update", "--pids-limit", "4096", computerContainerName("project")}, args)
					updates++
					if tc.fail {
						return nil, failure
					}
					limit = 4096
					return nil, nil
				default:
					t.Fatalf("unexpected Docker operation: %v", args)
					return nil, nil
				}
			}}
			_, err := p.resolve(t.Context(), "project", tc.current)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				// A later readiness check must not send another update.
				_, err = p.resolve(t.Context(), "project", tc.current)
				require.NoError(t, err)
			}
			if tc.fail {
				require.ErrorIs(t, err, failure)
			}
			require.Equal(t, tc.updates, updates)
		})
	}
}
