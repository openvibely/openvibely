package service

import (
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// Adapters for tests of the production entry points.

// formatThreadTranscript formats a task's execution history as a readable thread transcript.
// offset/limit control pagination: offset is the execution index to start from (0-based),
// limit is the max number of executions to include (0 = all that fit).
func formatThreadTranscript(task *models.Task, executions []models.Execution, offset, limit int) string {
	total := len(executions)
	if offset > 0 {
		if offset >= total {
			return formatThreadTranscriptPage(task, nil, total, offset).transcript
		}
		executions = executions[offset:]
	}
	if limit > 0 && limit < len(executions) {
		executions = executions[:limit]
	}
	return formatThreadTranscriptWithTotal(task, executions, total, offset)
}

// getProjectChanges runs git commands against a project's repo to gather change data
func (s *UpcomingService) getProjectChanges(repoPath string, since time.Time) (*models.ProjectChanges, error) {
	pc, _, err := s.getProjectChangesInRangeWithFiles(repoPath, since, time.Time{})
	return pc, err
}

func xURLIDNADomainValid(domain string) bool {
	_, ok := xURLIDNAEncodedDomain(domain)
	return ok
}
