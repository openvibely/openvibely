package prompt

import (
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// CodexExecArgs builds args for task execution (ephemeral, no session persistence).
func CodexExecArgs(model, configuredEffort string, imagePaths []string) []string {
	model = CodexModelOrDefault(model)
	effort := CodexReasoningEffort(model, configuredEffort)
	args := []string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"--ephemeral",
		"-m", model,
		"-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort),
		"-c", fmt.Sprintf(`reasoning.effort="%s"`, effort),
	}
	for _, img := range imagePaths {
		args = append(args, "--image", img)
	}
	args = append(args, "-")
	return args
}

// CodexChatArgs builds args for first chat message (persists session for resume).
func CodexChatArgs(model, configuredEffort string, imagePaths []string, chatMode models.ChatMode) []string {
	model = CodexModelOrDefault(model)
	effort := CodexReasoningEffort(model, configuredEffort)
	args := []string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"-m", model,
		"-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort),
		"-c", fmt.Sprintf(`reasoning.effort="%s"`, effort),
	}
	if chatMode == models.ChatModePlan {
		args = append(args, "-c", `collaboration_mode="plan"`)
	}
	for _, img := range imagePaths {
		args = append(args, "--image", img)
	}
	args = append(args, "-")
	return args
}

// CodexResumeArgs builds args to resume an existing chat thread.
func CodexResumeArgs(model, configuredEffort, threadID string, imagePaths []string, chatMode models.ChatMode) []string {
	model = CodexModelOrDefault(model)
	effort := CodexReasoningEffort(model, configuredEffort)
	args := []string{
		"exec", "resume", threadID,
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"--skip-git-repo-check",
		"-m", model,
		"-c", fmt.Sprintf(`model_reasoning_effort="%s"`, effort),
		"-c", fmt.Sprintf(`reasoning.effort="%s"`, effort),
	}
	if chatMode == models.ChatModePlan {
		args = append(args, "-c", `collaboration_mode="plan"`)
	}
	for _, img := range imagePaths {
		args = append(args, "--image", img)
	}
	args = append(args, "-")
	return args
}
