package remote

import (
	"context"
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestLocalCompletionWithRemoteReferences(t *testing.T) {
	for _, viaTask := range []bool{false, true} {
		name := "execution"
		if viaTask {
			name = "task"
		}
		t.Run(name, func(t *testing.T) {
			local := teststores.New(t).Stores
			stores := Wrap(local, &Router{})
			// An unrelated remote page context must not override an explicit local ID.
			ctx := WithServer(context.Background(), "fedcba9876543210")
			output := "Test log: /chat?project_id=" + remoteid.ID(server, "17ceb33b5ae1af9743752f537f68cb33")
			task := &models.Task{ProjectID: "default", Title: "Existing local task", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: output}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: output, IsFollowup: true}
			require.NoError(t, stores.Executions.Create(ctx, execution))
			require.NoError(t, stores.Executions.AppendOutput(ctx, execution.ID, output))
			if viaTask {
				outcome, err := stores.Tasks.CompleteExecutionSuccess(ctx, execution.ID, output, 12, 100)
				require.NoError(t, err)
				require.Equal(t, persistence.CompleteSuccessCompleted, outcome)
			} else {
				require.NoError(t, stores.Executions.Complete(ctx, execution.ID, models.ExecCompleted, output, "", 12, 100))
			}
			_, err := stores.Executions.RecoverStaleRunningTaskExecutions(context.Background())
			require.NoError(t, err)
			got, err := local.Executions.GetByID(context.Background(), execution.ID)
			require.NoError(t, err)
			require.Equal(t, models.ExecCompleted, got.Status)
			require.Equal(t, output, got.Output)
			require.Empty(t, got.ErrorMessage)
		})
	}
}

func TestStoreRoutingIgnoresPayloads(t *testing.T) {
	remoteID := remoteid.ID(server, "entity")
	cases := []struct {
		store, method string
		args          []any
		want          string
	}{
		{"Executions", "Complete", []any{"local", models.ExecFailed, remoteID, remoteID, 0, int64(0)}, ""},
		{"Tasks", "CompleteExecutionSuccess", []any{"local", remoteID, 0, int64(0)}, ""},
		{"Tasks", "CompleteExecutionSuccess", []any{remoteID, remoteid.ID("fedcba9876543210", "other"), 0, int64(0)}, server},
		{"Tasks", "Create", []any{&models.Task{ProjectID: "default", Prompt: remoteID, Title: remoteID}}, ""},
		{"Tasks", "Create", []any{&models.Task{ProjectID: remoteID, Prompt: remoteid.ID("fedcba9876543210", "other")}}, server},
		{"Executions", "GetByID", []any{remoteID}, server},
		{"Executions", "ReplayRowsByExecutionIDs", []any{[]string{remoteID}}, server},
		{"Executions", "AppendEncodedReplayChanges", []any{models.ConversationOwner{ExecutionID: "local"}, nil}, ""},
	}
	for _, tc := range cases {
		got, mixed := argServer(routingIDs(tc.store, tc.method, tc.args))
		require.False(t, mixed, "%s.%s", tc.store, tc.method)
		require.Equal(t, tc.want, got, "%s.%s", tc.store, tc.method)
	}
	_, mixed := argServer(routingIDs("Executions", "GetByIDForProject", []any{remoteID, remoteid.ID("fedcba9876543210", "p")}))
	require.True(t, mixed)
	// Remote mutations still go through the existing owner-side action path;
	// the read-only store transport must not silently write locally.
	var err error
	require.True(t, (&Router{}).Remote(context.Background(), "Executions", "Complete", []any{remoteID, models.ExecCompleted, "local output", "", 0, int64(0)}, nil, &err))
	require.ErrorIs(t, err, ErrUnsupported)
}

func TestLocalFailurePreservesRemoteReferences(t *testing.T) {
	stores := Wrap(teststores.New(t).Stores, &Router{})
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "Failed local task", Category: models.CategoryActive, Status: models.StatusRunning}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, stores.Executions.Create(ctx, execution))
	output := remoteid.ID(server, "remote-project")
	message := "Could not access " + remoteid.ID("fedcba9876543210", "other-project")
	require.NoError(t, stores.Executions.Complete(ctx, execution.ID, models.ExecFailed, output, message, 1, 2))
	got, err := stores.Executions.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, got.Status)
	require.Equal(t, output, got.Output)
	require.Equal(t, message, got.ErrorMessage)
}

func TestPrepareArgsPreservesFreeText(t *testing.T) {
	paramNames["TestPayload"] = map[string][]string{"Search": {"projectID", "query"}}
	defer delete(paramNames, "TestPayload")
	text := "look for " + remoteid.ID(server, "p")
	payload, err := prepareArgs("TestPayload", "Search", server, []any{remoteid.ID(server, "p"), text}, false)
	require.NoError(t, err)
	values, err := decodeValues(payload, []reflect.Type{reflect.TypeOf(""), reflect.TypeOf("")})
	require.NoError(t, err)
	require.Equal(t, "p", values[0].String())
	require.Equal(t, text, values[1].String())
}
