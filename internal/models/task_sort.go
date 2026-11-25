package models

import (
	"sort"
	"strings"
	"time"
)

// SortTasksWithCategorySorts applies the board ordering to a complete list,
// including lists assembled from multiple servers.
func SortTasksWithCategorySorts(tasks []Task, category, backlogSort, completedSort string) {
	sort.SliceStable(tasks, func(i, j int) bool { return taskDisplayOrderAsc(tasks[i], tasks[j]) })
	switch category {
	case string(CategoryBacklog):
		sortTasks(tasks, backlogSort)
	case string(CategoryCompleted):
		sortTasks(tasks, completedSort)
	default:
		sortCategoryTasks(tasks, CategoryBacklog, backlogSort)
		sortCategoryTasks(tasks, CategoryCompleted, completedSort)
	}
}

func sortCategoryTasks(tasks []Task, category TaskCategory, sortBy string) {
	if len(tasks) < 2 || sortBy == "" {
		return
	}

	categoryIndexes := make([]int, 0, len(tasks))
	categoryTasks := make([]Task, 0, len(tasks))
	for i, task := range tasks {
		if task.Category == category {
			categoryIndexes = append(categoryIndexes, i)
			categoryTasks = append(categoryTasks, task)
		}
	}
	if len(categoryTasks) < 2 {
		return
	}

	sortTasks(categoryTasks, sortBy)
	for i, idx := range categoryIndexes {
		tasks[idx] = categoryTasks[i]
	}
}

func sortTasks(tasks []Task, sortBy string) {
	if len(tasks) < 2 || sortBy == "" {
		return
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		return taskSortLess(tasks[i], tasks[j], sortBy)
	})
}

func taskSortLess(a Task, b Task, sortBy string) bool {
	switch sortBy {
	case "title_asc":
		aTitle := strings.ToLower(a.Title)
		bTitle := strings.ToLower(b.Title)
		if aTitle != bTitle {
			return aTitle < bTitle
		}
		return taskDisplayOrderAsc(a, b)
	case "title_desc":
		aTitle := strings.ToLower(a.Title)
		bTitle := strings.ToLower(b.Title)
		if aTitle != bTitle {
			return aTitle > bTitle
		}
		return taskDisplayOrderAsc(a, b)
	case "created_asc":
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return taskDisplayOrderAsc(a, b)
	case "created_desc":
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return taskDisplayOrderDesc(a, b)
	case "completed_asc":
		aTime := completedSortTime(a)
		bTime := completedSortTime(b)
		if !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}
		return taskDisplayOrderAsc(a, b)
	case "completed_desc":
		aTime := completedSortTime(a)
		bTime := completedSortTime(b)
		if !aTime.Equal(bTime) {
			return aTime.After(bTime)
		}
		return taskDisplayOrderDesc(a, b)
	case "priority_asc":
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return taskDisplayOrderAsc(a, b)
	case "priority_desc":
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return taskDisplayOrderDesc(a, b)
	default:
		return taskDisplayOrderAsc(a, b)
	}
}

// completedSortTime returns CompletedAt if set, falling back to UpdatedAt.
// Tasks in the completed category always have CompletedAt set after migration 096.
func completedSortTime(t Task) time.Time {
	if t.CompletedAt != nil {
		return *t.CompletedAt
	}
	return t.UpdatedAt
}

func taskDisplayOrderAsc(a Task, b Task) bool {
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder < b.DisplayOrder
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

func taskDisplayOrderDesc(a Task, b Task) bool {
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder > b.DisplayOrder
	}
	return a.CreatedAt.After(b.CreatedAt)
}
