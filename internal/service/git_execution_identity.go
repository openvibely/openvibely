package service

import "context"

func (s *LLMService) SetGitCredentials(github *GitHubService) { s.gitCredentials = github }
func (s *LLMService) gitContext(ctx context.Context) context.Context {
	if s.gitCredentials != nil {
		return s.gitCredentials.GitContext(ctx)
	}
	return ctx
}
