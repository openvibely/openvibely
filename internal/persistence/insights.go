package persistence

import (
	"errors"
)

var (
	ErrInsightNotFound   = errors.New("insight not found")
	ErrKnowledgeNotFound = errors.New("knowledge entry not found")
)

type InsightFailedTaskPattern struct {
	Title     string
	FailCount int
	LastError string
}
