package service

import llmgitdiff "github.com/openvibely/openvibely/internal/llm/gitdiff"

// CaptureGitDiff runs `git diff HEAD` in the given directory to capture
// all uncommitted changes made during task execution. Returns the diff
// output or empty string if no changes or git is not available.
func (s *LLMService) CaptureGitDiff(workDir string) string {
	return llmgitdiff.Capture(workDir)
}
