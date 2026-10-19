package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

// IsTaskThread reports whether a task's conversation is a task thread rather
// than the project Chat page. The persisted category, not the upload location,
// decides which attachment table owns its message attachments.
func IsTaskThread(task *models.Task) bool {
	return task != nil && task.Category != models.CategoryChat
}

// swarmSharedAttachmentParent returns the swarm parent whose task attachments
// are shared inputs for task, or nil.
func swarmSharedAttachmentParent(ctx context.Context, taskRepo persistence.TaskStore, task *models.Task) (*models.Task, error) {
	if taskRepo == nil || task.SwarmRole == "" || task.ParentTaskID == nil {
		return nil, nil
	}
	parent, err := taskRepo.GetByID(ctx, *task.ParentTaskID)
	if err != nil {
		return nil, err
	}
	if parent == nil || parent.ProjectID != task.ProjectID || parent.SwarmRole != models.SwarmRoleParent {
		return nil, nil
	}
	return parent, nil
}

func listTaskConversationAttachments(ctx context.Context, attachmentRepo persistence.AttachmentStore, taskRepo persistence.TaskStore, task *models.Task) ([]models.Attachment, error) {
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	parent, err := swarmSharedAttachmentParent(ctx, taskRepo, task)
	if err != nil || parent == nil {
		return attachments, err
	}
	shared, err := attachmentRepo.ListByTask(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	return append(attachments, shared...), nil
}

// NewTaskAttachmentContextSource builds the task-wide attachment context from
// task_attachments. It reloads only after an attachment of the task (or of its
// swarm parent) is added or deleted.
func NewTaskAttachmentContextSource(attachmentRepo persistence.AttachmentStore, taskRepo persistence.TaskStore, task models.Task) *attachmentcontext.Source {
	keys := []string{task.ID}
	if task.SwarmRole != "" && task.ParentTaskID != nil {
		keys = append(keys, *task.ParentTaskID)
	}
	return attachmentcontext.NewSource(func(ctx context.Context) (string, error) {
		attachments, err := listTaskConversationAttachments(ctx, attachmentRepo, taskRepo, &task)
		if err != nil {
			return "", fmt.Errorf("load task attachments: %w", err)
		}
		return TaskAttachmentReferences(attachments), nil
	}, keys...)
}

func TaskAttachmentReferences(attachments []models.Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, att := range attachments {
		sb.WriteString(llmattachment.Reference(att))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (s *LLMService) taskAttachmentUploadsRoot() string {
	if s.taskSvc != nil && strings.TrimSpace(s.taskSvc.uploadsDir) != "" {
		return s.taskSvc.uploadsDir
	}
	return "uploads"
}

// publishTaskSteeringAttachments copies steering uploads into task_attachments,
// owned by the execution that consumed the steering message. Pending upload
// directories are left in place; the caller must call finish(true) after the
// steering commit succeeds, or finish(false) to undo the publication so a
// retried delivery can publish again from the still-present uploads.
func (s *LLMService) publishTaskSteeringAttachments(ctx context.Context, taskID, execID string, inputs []models.ThreadInput) (finish func(committed bool), err error) {
	if s.attachmentRepo == nil {
		return nil, fmt.Errorf("attachment repository is unavailable")
	}
	root := s.taskAttachmentUploadsRoot()
	type published struct{ id, path string }
	var done []published
	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			if err := s.attachmentRepo.Delete(context.WithoutCancel(ctx), done[i].id); err == nil {
				_ = os.Remove(done[i].path)
			}
		}
	}
	var sessions []string
	for _, input := range inputs {
		sessionID := strings.TrimSpace(input.AttachmentSessionID)
		if sessionID == "" {
			continue
		}
		source := filepath.Join(root, "chat", "pending", sessionID)
		entries, err := os.ReadDir(source)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("reading steering attachments %s: %w", sessionID, err)
		}
		dest := filepath.Join(root, "chat", execID)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			rollback()
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			destPath := filepath.Join(dest, entry.Name())
			if err := copyAttachmentFile(filepath.Join(source, entry.Name()), destPath); err != nil {
				rollback()
				return nil, fmt.Errorf("publishing steering attachment %s: %w", entry.Name(), err)
			}
			info, err := os.Stat(destPath)
			if err != nil {
				_ = os.Remove(destPath)
				rollback()
				return nil, err
			}
			att := &models.Attachment{TaskID: taskID, ExecutionID: execID, FileName: entry.Name(), FilePath: destPath, MediaType: taskSteeringImageMediaType(entry.Name()), FileSize: info.Size()}
			if err := s.attachmentRepo.Create(ctx, att); err != nil {
				_ = os.Remove(destPath)
				rollback()
				return nil, err
			}
			done = append(done, published{id: att.ID, path: destPath})
		}
		sessions = append(sessions, source)
	}
	return func(committed bool) {
		if !committed {
			rollback()
			return
		}
		for _, source := range sessions {
			_ = os.RemoveAll(source)
		}
	}, nil
}

func copyAttachmentFile(srcPath, destPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".attachment-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), destPath); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
