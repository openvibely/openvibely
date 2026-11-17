package persistence

import (
	"errors"
)

var (
	ErrInsightNotFound = errors.New("insight not found")
)

type InsightFailedTaskPattern struct {
	Title     string
	FailCount int
	LastError string
}
