package models

import "time"

// MessageAttachmentSourceTask marks a message attachment row stored in
// task_attachments (task threads) rather than chat_attachments (Chat page).
const MessageAttachmentSourceTask = "task"

type ChatAttachment struct {
	ID          string    `json:"id"`
	ExecutionID string    `json:"execution_id"`
	FileName    string    `json:"file_name"`
	FilePath    string    `json:"file_path"`
	MediaType   string    `json:"media_type"`
	FileSize    int64     `json:"file_size"`
	CreatedAt   time.Time `json:"created_at"`
	// Source is a display-only marker; empty means chat_attachments.
	Source string `json:"-"`
}

// MessageAttachmentFromTask presents a task-thread message attachment in the
// per-message view shared by Chat and task threads.
func MessageAttachmentFromTask(att Attachment) ChatAttachment {
	return ChatAttachment{
		ID: att.ID, ExecutionID: att.ExecutionID, FileName: att.FileName, FilePath: att.FilePath,
		MediaType: att.MediaType, FileSize: att.FileSize, CreatedAt: att.CreatedAt,
		Source: MessageAttachmentSourceTask,
	}
}
