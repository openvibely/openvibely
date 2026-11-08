package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

type forkPRProvider struct {
	*fakeTaskPullRequestGitHubProvider
	parent *GitHubRepoRef
}

func (p *forkPRProvider) RepositoryOptions(context.Context, *GitHubRepoRef) (*GitHubRepositoryOptions, error) {
	return &GitHubRepositoryOptions{DefaultBranch: "main", Parent: p.parent}, nil
}
func (p *forkPRProvider) RepositoryBranches(context.Context, *GitHubRepoRef, int) ([]string, bool, error) {
	return []string{"main"}, false, nil
}

func TestForkPullRequestPublishesToForkAndKeepsLinkedDestination(t *testing.T) {
	ctx := context.Background()
	env := teststores.New(t)
	project := &models.Project{Name: "Fork", RepoPath: t.TempDir(), RepoURL: "https://github.com/contributor/fork"}
	if err := env.Stores.Projects.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Contribution", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/contribution", MergeTargetBranch: "main"}
	if err := env.Stores.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	source, _ := ParseGitHubRepoURL(project.RepoURL)
	target, _ := ParseGitHubRepoURL("https://github.com/maintainer/original")
	sha := strings.Repeat("a", 40)
	createCount, publishCount := 0, 0
	pr := &GitHubPullRequest{Number: 7, URL: target.HTMLURL + "/pull/7", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: source.FullName, HeadSHA: sha}
	provider := &forkPRProvider{parent: &target, fakeTaskPullRequestGitHubProvider: &fakeTaskPullRequestGitHubProvider{
		resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) { return &source, nil },
		publishBranchFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			publishCount++
			if repo.FullName != source.FullName {
				t.Fatalf("published to upstream: %s", repo.FullName)
			}
			if !req.PreserveLocalHistory || req.LocalBaseBranch != "main" {
				t.Fatalf("fork publication must preserve local history: %+v", req)
			}
			return &GitHubPublishBranchResult{HeadSHA: sha}, nil
		},
		findPRFn: func(_ context.Context, repo *GitHubRepoRef, head string) (*GitHubPullRequest, error) {
			if repo.FullName != target.FullName || head != "contributor:"+task.WorktreeBranch {
				t.Fatalf("wrong PR lookup: %s %s", repo.FullName, head)
			}
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCount++
			if repo.FullName != target.FullName || req.Head != "contributor:"+task.WorktreeBranch || req.HeadRepo != "fork" || req.Base != "release" {
				t.Fatalf("wrong PR destination: %s %+v", repo.FullName, req)
			}
			return pr, nil
		},
		getPullRequestFn: func(_ context.Context, repo *GitHubRepoRef, n int) (*GitHubPullRequest, error) {
			if repo.FullName != target.FullName || n != 7 {
				t.Fatalf("linked PR read from wrong repo: %s #%d", repo.FullName, n)
			}
			return pr, nil
		},
		replaceBranchHeadFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) (string, error) {
			if repo.FullName != source.FullName {
				t.Fatal("replaced upstream branch")
			}
			return sha, nil
		},
	}}
	svc := NewTaskPullRequestService(provider, env.Stores.TaskPullRequests)
	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{TargetRepoURL: target.HTMLURL, Base: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("expected new PR")
	}
	record, err := env.Stores.TaskPullRequests.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.HeadRepoURL != source.HTMLURL || record.BaseBranch != "release" || record.PRURL != pr.URL {
		t.Fatalf("routing metadata lost: %+v", record)
	}
	// Changing project preferences must not redirect a linked PR on the next publication.
	result, err = svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{TargetRepoURL: source.HTMLURL, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReusedExistingRecord || createCount != 1 || publishCount != 2 {
		t.Fatalf("PR was recreated: creates=%d publishes=%d", createCount, publishCount)
	}
	_, err = svc.ReplaceBranchHeadForTask(ctx, project, &models.Task{ID: task.ID, ProjectID: project.ID, WorktreeBranch: task.WorktreeBranch, WorktreePath: project.RepoPath}, sha)
	if err != nil {
		t.Fatal(err)
	}
}

func TestForkPullRequestRejectsUnrelatedTargetBeforePublishing(t *testing.T) {
	for _, targetURL := range []string{"https://github.com/unrelated/repo", "https://evil.example/maintainer/original"} {
		t.Run(targetURL, func(t *testing.T) {
			env := teststores.New(t)
			source, _ := ParseGitHubRepoURL("https://github.com/contributor/fork")
			parent, _ := ParseGitHubRepoURL("https://github.com/maintainer/original")
			provider := &forkPRProvider{parent: &parent, fakeTaskPullRequestGitHubProvider: &fakeTaskPullRequestGitHubProvider{
				resolveRepoFn: func(context.Context, string, string) (*GitHubRepoRef, error) { return &source, nil },
				publishBranchFn: func(context.Context, *GitHubRepoRef, GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
					t.Fatal("published before validation")
					return nil, fmt.Errorf("unexpected publication")
				},
			}}
			_, err := NewTaskPullRequestService(provider, env.Stores.TaskPullRequests).OpenForTask(context.Background(), &models.Project{RepoPath: t.TempDir(), RepoURL: source.HTMLURL}, &models.Task{ID: "missing", WorktreeBranch: "task/example"}, OpenTaskPullRequestOptions{TargetRepoURL: targetURL})
			if err == nil {
				t.Fatal("accepted unrelated target")
			}
		})
	}
}
