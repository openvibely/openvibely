package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type namingObservedStore struct {
	persistence.TaskStore
	saved chan bool
}

func (s *namingObservedStore) UpdateGeneratedTitle(ctx context.Context, id, old, prompt, title string) (bool, error) {
	changed, err := s.TaskStore.UpdateGeneratedTitle(ctx, id, old, prompt, title)
	s.saved <- changed
	return changed, err
}

func TestNamingAsyncIndependentAndPreservesManualRename(t *testing.T) {
	env := teststores.New(t)
	store := &namingObservedStore{TaskStore: env.Stores.Tasks, saved: make(chan bool, 2)}
	svc := NewLLMService(env.Stores.LLMConfigs, nil, store, nil, nil, nil)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model", Name: "Naming"}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	mock := &testutil.MockLLMCaller{Response: `{"title":"Generated title"}`, OnCall: func(ctx context.Context, _ testutil.MockLLMCall) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}}
	svc.SetLLMCaller(mock)
	a := models.Task{ProjectID: "default", Title: "First request", Prompt: "First request", Category: models.CategoryBacklog, Status: models.StatusPending}
	b := models.Task{ProjectID: "default", Title: "Second request", Prompt: "Second request", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, store.Create(t.Context(), &a))
	require.NoError(t, store.Create(t.Context(), &b))
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // Finishing the HTTP request must not cancel naming.
	svc.NameTaskAsync(ctx, a, &agent)
	svc.NameTaskAsync(ctx, b, &agent)
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("naming serialized or canceled with request")
		}
	}
	a.Title = "My manual name"
	require.NoError(t, store.Update(t.Context(), &a))
	close(release)
	changed := 0
	for i := 0; i < 2; i++ {
		select {
		case ok := <-store.saved:
			if ok {
				changed++
			}
		case <-time.After(5 * time.Second):
			t.Fatal("naming did not finish")
		}
	}
	require.Equal(t, 1, changed)
	gotA, err := store.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	gotB, err := store.GetByID(t.Context(), b.ID)
	require.NoError(t, err)
	require.Equal(t, "My manual name", gotA.Title)
	require.Equal(t, "Generated title", gotB.Title)
	require.Equal(t, models.StatusPending, gotB.Status)
	require.Equal(t, agent.Model, mock.LastCall().Agent.Model)
}

func TestNamingCanceledWhileWaitingForCapacity(t *testing.T) {
	for i := 0; i < cap(namingSlots); i++ {
		namingSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(namingSlots); i++ {
			<-namingSlots
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &LLMService{}
	require.Empty(t, svc.generateName(ctx, "prompt", models.LLMConfig{}, "", 60))
}

func TestNamingFallbackAndValidation(t *testing.T) {
	require.Equal(t, "Fix login", ProvisionalTaskTitle("\n  Fix   login\nExtra detail"))
	title := ProvisionalTaskTitle(strings.Repeat("界", 70))
	require.True(t, utf8.ValidString(title))
	require.Equal(t, 60, utf8.RuneCountInString(title))
	require.Empty(t, parseGeneratedName(`{"title":"a","extra":true}`, 60))
	require.Empty(t, parseGeneratedName(`{"title":"a"} {"title":"b"}`, 60))
	require.Empty(t, parseGeneratedName(`{"title":"a\nb"}`, 60))
	require.Empty(t, parseGeneratedName(`{"title":"`+strings.Repeat("a", 61)+`"}`, 60))
	require.Equal(t, "界", parseGeneratedName(`{"title":"界"}`, 1))
	require.Contains(t, taskNamePrompt(strings.Repeat("a", 600)), strings.Repeat("a", 500))
	require.NotContains(t, taskNamePrompt(strings.Repeat("a", 600)), strings.Repeat("a", 501))
}

func TestNamingProviderFailureLeavesFallbackAvailable(t *testing.T) {
	env := teststores.New(t)
	svc := NewLLMService(env.Stores.LLMConfigs, nil, nil, nil, nil, nil)
	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model", Name: "Naming"}
	for _, output := range []string{"not JSON", `{"title":""}`, `{"title":42}`} {
		svc.SetLLMCaller(&testutil.MockLLMCaller{Response: output})
		require.Empty(t, svc.generateName(t.Context(), taskNamePrompt("Fix login"), agent, "", 60))
	}
}

func TestNamingDoesNotUpdateDeletedTaskOrChangedPrompt(t *testing.T) {
	env := teststores.New(t)
	store := env.Stores.Tasks
	task := &models.Task{ProjectID: "default", Title: "Provisional", Prompt: "Old request", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, store.Create(t.Context(), task))
	task.Prompt = "Revised request"
	require.NoError(t, store.Update(t.Context(), task))
	changed, err := store.UpdateGeneratedTitle(t.Context(), task.ID, "Provisional", "Old request", "Stale title")
	require.NoError(t, err)
	require.False(t, changed)
	require.NoError(t, store.Delete(t.Context(), task.ID))
	changed, err = store.UpdateGeneratedTitle(t.Context(), task.ID, "Provisional", "Revised request", "Deleted task")
	require.NoError(t, err)
	require.False(t, changed)
}

func TestNamingSurvivesStaleNonTitleSave(t *testing.T) {
	env := teststores.New(t)
	store := env.Stores.Tasks
	svc := NewTaskService(store, nil, nil)
	task := &models.Task{ProjectID: "default", Title: "Provisional", Prompt: "Request", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, store.Create(t.Context(), task))
	stale, err := store.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	updated, err := store.UpdateGeneratedTitle(t.Context(), task.ID, task.Title, task.Prompt, "Generated")
	require.NoError(t, err)
	require.True(t, updated)
	stale.Priority = 4
	require.NoError(t, svc.UpdatePreservingTitle(t.Context(), stale))
	current, err := store.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "Generated", current.Title)
	require.Equal(t, 4, current.Priority)
	current.Title = "Explicit rename"
	require.NoError(t, svc.Update(t.Context(), current))
	current, err = store.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "Explicit rename", current.Title)
}

func TestNamingPreservesExplicitTitleEvenWhenTextMatchesProvisional(t *testing.T) {
	for _, changeAway := range []bool{false, true} {
		t.Run(fmt.Sprint(changeAway), func(t *testing.T) {
			env := teststores.New(t)
			store := env.Stores.Tasks
			task := &models.Task{ProjectID: "default", Title: "Fix login", Prompt: "Fix login", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
			require.NoError(t, store.Create(t.Context(), task))
			if changeAway {
				task.Title = "My title"
				require.NoError(t, store.Update(t.Context(), task))
			}
			task.Title = "Fix login"
			require.NoError(t, store.Update(t.Context(), task))
			changed, err := store.UpdateGeneratedTitle(t.Context(), task.ID, "Fix login", "Fix login", "Automatic replacement")
			require.NoError(t, err)
			require.False(t, changed)
			current, err := store.GetByID(t.Context(), task.ID)
			require.NoError(t, err)
			require.Equal(t, "Fix login", current.Title)
		})
	}
}
