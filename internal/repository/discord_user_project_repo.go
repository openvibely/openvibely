package repository

import (
	"context"
	"database/sql"
)

// DiscordUserProjectRepo persists active project selection per Discord user.
type DiscordUserProjectRepo struct {
	db *sql.DB
}

func NewDiscordUserProjectRepo(db *sql.DB) *DiscordUserProjectRepo {
	return &DiscordUserProjectRepo{db: db}
}

func (r *DiscordUserProjectRepo) SetUserProject(ctx context.Context, discordUserID, projectID string) error {
	return upsertUserProject(ctx, r.db, "discord_user_projects", "discord_user_id", discordUserID, projectID, "discord user project")
}

func (r *DiscordUserProjectRepo) GetUserProject(ctx context.Context, discordUserID string) (string, error) {
	return getUserProject(ctx, r.db, "discord_user_projects", "discord_user_id", discordUserID, "discord user project")
}

func (r *DiscordUserProjectRepo) DeleteUserProject(ctx context.Context, discordUserID string) error {
	return deleteUserProject(ctx, r.db, "discord_user_projects", "discord_user_id", discordUserID, "discord user project")
}
