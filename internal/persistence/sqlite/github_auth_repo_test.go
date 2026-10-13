package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestGitHubAuthRepoAuthorizedActorsDenyByDefaultAndNormalize(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubAuthRepo(db)
	ctx := context.Background()

	authorized, err := repo.IsActorAuthorized(ctx, "alice")
	if err != nil {
		t.Fatalf("check empty authorization: %v", err)
	}
	if authorized {
		t.Fatal("expected empty GitHub authorized actors list to deny by default")
	}

	userID := int64(12345)
	actor := &models.GitHubAuthorizedActor{
		GitHubUserID: &userID,
		GitHubLogin:  " @Alice ",
		DisplayName:  "Alice",
		Permission:   "approve",
		AddedBy:      "test",
	}
	if err := repo.UpsertAuthorizedActor(ctx, actor); err != nil {
		t.Fatalf("upsert authorized actor: %v", err)
	}
	if actor.GitHubLogin != "alice" {
		t.Fatalf("expected normalized login alice, got %q", actor.GitHubLogin)
	}

	for _, login := range []string{"alice", "ALICE", " Alice ", "@alice", " @ALICE "} {
		authorized, err := repo.IsActorAuthorized(ctx, login)
		if err != nil {
			t.Fatalf("check authorization for %q: %v", login, err)
		}
		if !authorized {
			t.Fatalf("expected %q to be authorized", login)
		}
	}
	if authorized, err := repo.IsActorAuthorized(ctx, "bob"); err != nil {
		t.Fatalf("check unauthorized actor: %v", err)
	} else if authorized {
		t.Fatal("expected unknown GitHub actor to be unauthorized")
	}

	updated := &models.GitHubAuthorizedActor{GitHubLogin: "@ALICE", DisplayName: "Alice Updated", Permission: "admin", AddedBy: "test-update"}
	if err := repo.UpsertAuthorizedActor(ctx, updated); err != nil {
		t.Fatalf("update authorized actor: %v", err)
	}
	actors, err := repo.ListAuthorizedActors(ctx)
	if err != nil {
		t.Fatalf("list authorized actors: %v", err)
	}
	if len(actors) != 1 {
		t.Fatalf("expected one authorized actor, got %d", len(actors))
	}
	if actors[0].GitHubLogin != "alice" || actors[0].DisplayName != "Alice Updated" || actors[0].Permission != "admin" {
		t.Fatalf("unexpected authorized actor after update: %#v", actors[0])
	}
	if actors[0].GitHubUserID == nil || *actors[0].GitHubUserID != userID {
		t.Fatalf("expected preserved github user id %d, got %#v", userID, actors[0].GitHubUserID)
	}

	if err := repo.DeleteAuthorizedActor(ctx, actor.ID); err != nil {
		t.Fatalf("delete authorized actor: %v", err)
	}
	if authorized, err := repo.IsActorAuthorized(ctx, "alice"); err != nil {
		t.Fatalf("check deleted authorization: %v", err)
	} else if authorized {
		t.Fatal("expected deleted actor to be unauthorized")
	}
}

func TestGitHubAuthRepoProjectInboxStoresSimpleScheduledTaskAssignee(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewGitHubAuthRepo(db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES ('proj-github-inbox-one', 'One', '', '/tmp/one')`); err != nil {
		t.Fatalf("insert project one: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES ('proj-github-inbox-two', 'Two', '', '/tmp/two')`); err != nil {
		t.Fatalf("insert project two: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, name, key, scope) VALUES ('agent-github-dev', 'GitHub Dev', 'github_dev', 'project')`); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	missing, err := repo.GetEnabledProjectInbox(ctx, "proj-github-inbox-one")
	if err != nil {
		t.Fatalf("get missing inbox: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected no inbox before configuration, got %#v", missing)
	}

	agentID := "agent-github-dev"
	userID := int64(999)
	inbox := &models.GitHubProjectInbox{
		ProjectID:    "proj-github-inbox-one",
		GitHubUserID: &userID,
		GitHubLogin:  " @Dev-Bot ",
		AgentID:      &agentID,
		Enabled:      true,
	}
	if err := repo.UpsertProjectInbox(ctx, inbox); err != nil {
		t.Fatalf("upsert project inbox: %v", err)
	}
	if inbox.GitHubLogin != "dev-bot" {
		t.Fatalf("expected normalized inbox login dev-bot, got %q", inbox.GitHubLogin)
	}

	got, err := repo.GetEnabledProjectInbox(ctx, "proj-github-inbox-one")
	if err != nil {
		t.Fatalf("get enabled inbox: %v", err)
	}
	if got == nil {
		t.Fatal("expected enabled inbox")
	}
	if got.GitHubLogin != "dev-bot" || got.AgentID == nil || *got.AgentID != agentID || got.GitHubUserID == nil || *got.GitHubUserID != userID {
		t.Fatalf("unexpected inbox: %#v", got)
	}

	if err := repo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{ProjectID: "proj-github-inbox-two", GitHubLogin: "DEV-BOT", Enabled: true}); err != nil {
		t.Fatalf("same GitHub assignee should be usable by another project: %v", err)
	}
	if err := repo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{ProjectID: "proj-github-inbox-one", GitHubLogin: "Other-Bot", Enabled: false}); err != nil {
		t.Fatalf("disable/update project inbox: %v", err)
	}
	disabled, err := repo.GetProjectInbox(ctx, "proj-github-inbox-one")
	if err != nil {
		t.Fatalf("get disabled inbox: %v", err)
	}
	if disabled == nil || disabled.GitHubLogin != "other-bot" || disabled.Enabled {
		t.Fatalf("expected disabled updated inbox, got %#v", disabled)
	}
	enabled, err := repo.GetEnabledProjectInbox(ctx, "proj-github-inbox-one")
	if err != nil {
		t.Fatalf("get disabled enabled inbox: %v", err)
	}
	if enabled != nil {
		t.Fatalf("expected disabled inbox not to resolve as enabled, got %#v", enabled)
	}
}
