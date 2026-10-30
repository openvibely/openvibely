package handler

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestProjectQuestionAnswersValidateScopeAndSurvivePersistence(t *testing.T) {
	questions := []chatInputRequestQuestion{{ID: "scope", Kind: "projects", Question: "Which projects?", Options: []chatInputRequestOption{{Label: "All projects", Description: "All"}, {Label: "Specific projects", Description: "Select"}}, Projects: []events.ChatInputProject{{ID: "a", Name: "API"}, {ID: "b", Name: "Web"}}}}
	for _, tt := range []struct {
		name, label string
		ids         []string
		valid       bool
	}{
		{"one", "Specific projects", []string{"a"}, true},
		{"subset", "Specific projects", []string{"a", "b"}, true},
		{"all", "All projects", []string{"a", "b"}, true},
		{"empty", "Specific projects", nil, false},
		{"forged", "Specific projects", []string{"other"}, false},
		{"duplicate", "Specific projects", []string{"a", "a"}, false},
		{"partial all", "All projects", []string{"a"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			answer := []chatInputRequestAnswer{{QuestionID: "scope", Label: tt.label, ProjectIDs: tt.ids}}
			err := validateChatInputAnswers(questions, answer)
			if !tt.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			encoded, err := json.Marshal(cloneChatInputAnswers(answer))
			require.NoError(t, err)
			var restored []chatInputRequestAnswer
			require.NoError(t, json.Unmarshal(encoded, &restored))
			require.Equal(t, answer, restored)
		})
	}
	require.Error(t, validateChatInputAnswers(questions, []chatInputRequestAnswer{{QuestionID: "scope", CustomAnswer: "all"}}))
	clone := cloneChatInputQuestions(questions)
	clone[0].Projects[0].Name = "Changed"
	require.Equal(t, "API", questions[0].Projects[0].Name)
	require.Equal(t, questions[0].Projects, chatInputQuestionsForEvent(questions)[0].Projects)
}

func TestProjectQuestionToolLoadsServerCatalog(t *testing.T) {
	tc := NewStoreTestContext(t)
	project := tc.CreateProject().WithName("Project scope").Build()
	cb := events.NewChatBroadcaster()
	tc.handler.SetChatBroadcaster(cb)
	sub, err := cb.SubscribeProject(project.ID)
	require.NoError(t, err)
	defer cb.Unsubscribe(sub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan string, 1)
	go func() {
		out, err := tc.handler.executeRequestUserInputTool(ctx, streamingResponseParams{ProjectID: project.ID, ExecID: "scope", Surface: chatcontrol.SurfaceWeb}, json.RawMessage(`{"questions":[{"id":"projects","kind":"projects","question":"Which projects?"}]}`))
		if err != nil {
			result <- err.Error()
		} else {
			result <- out
		}
	}()
	select {
	case event := <-sub:
		q := event.InputRequest.Questions[0]
		require.Equal(t, "projects", q.Kind)
		require.Contains(t, q.Projects, events.ChatInputProject{ID: project.ID, Name: project.Name})
		require.Equal(t, "All projects", q.Options[0].Label)
		postInputRequestJSON(t, tc, "/chat/input-requests/"+event.InputRequest.ID+"/answer", `{"project_id":"`+project.ID+`","answers":[{"question_id":"projects","label":"Specific projects","project_ids":["`+project.ID+`"]}]}`, http.StatusOK)
	case <-time.After(3 * time.Second):
		t.Fatal("no project question event")
	}
	select {
	case out := <-result:
		require.Contains(t, out, `"project_ids":["`+project.ID+`"]`)
	case <-time.After(3 * time.Second):
		t.Fatal("no answer")
	}
}
