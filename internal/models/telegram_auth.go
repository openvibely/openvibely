package models

import "time"

// TelegramAuthorizedUser represents a Telegram user authorized to interact with a project's bot.
type TelegramAuthorizedUser struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	TelegramUserID   int64     `json:"telegram_user_id"`
	TelegramUsername string    `json:"telegram_username"`
	DisplayName      string    `json:"display_name"`
	AddedAt          time.Time `json:"added_at"`
	AddedBy          string    `json:"added_by"`
}
