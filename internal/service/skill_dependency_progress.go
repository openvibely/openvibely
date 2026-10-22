package service

import (
	"context"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"sync"
)

func (s *LLMService) withDependencyProgress(req llmcontracts.AgentRequest) context.Context {
	if s.broadcaster == nil || s.execRepo == nil || req.ExecID == "" || req.LifecycleHookCall {
		return req.Ctx
	}
	var once sync.Once
	var taskID string
	return skilldeps.WithProgress(req.Ctx, func(progress skilldeps.Progress) {
		once.Do(func() {
			if execution, err := s.execRepo.GetByID(req.Ctx, req.ExecID); err == nil && execution != nil {
				taskID = execution.TaskID
			}
		})
		if taskID == "" {
			return
		}
		s.broadcaster.Publish(events.TaskEvent{Type: events.SkillDependencyProgress, DependencyName: progress.Name, ProjectID: req.ProjectID, TaskID: taskID, ExecID: req.ExecID, Phase: progress.Stage, Message: progress.Name + ": " + progress.Stage})
	})
}
