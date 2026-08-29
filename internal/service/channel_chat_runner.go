package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

// ChannelChatRunRequest is the service-to-handler bridge for channel-origin
// Chat turns. Channel services own inbound channel parsing and reply metadata;
// the handler owns the shared steering-aware streaming runner.
type ChannelChatRunRequest struct {
	ExecID              string
	TaskID              string
	ProjectID           string
	Message             string
	Agent               models.LLMConfig
	ChatHistory         []models.Execution
	SystemContext       string
	WorkDir             string
	ImageAttachments    []models.Attachment
	Surface             chatcontrol.Surface
	InitialAckMessageID int
	ReplyContext        ChannelReplyContext
	// RuntimeTools holds channel-specific runtime tool definitions and executor.
	// When non-nil, the handler runner uses these tools for the chat turn instead
	// of rebuilding the generic handler runtime, so switch_project and other
	// channel-sensitive tools execute through the channel service handler.
	RuntimeTools *llmcontracts.RuntimeTools
}

type ChannelChatRunner func(context.Context, ChannelChatRunRequest)

type ChannelTaskRunRequest struct {
	ExecID          string
	TaskID          string
	ProjectID       string
	Message         string
	Agent           models.LLMConfig
	ChatHistory     []models.Execution
	SystemContext   string
	Surface         chatcontrol.Surface
	AgentDefinition *models.Agent
	ReplyContext    ChannelReplyContext
}

type ChannelReplyContext struct {
	Source           string
	TelegramChatID   int64
	SlackTeamID      string
	SlackChannelID   string
	SlackThreadTS    string
	SlackUserID      string
	EmailFrom        string
	EmailMessageID   string
	EmailReferences  string
	EmailSubject     string
	EmailSessionKey  string
	DiscordChannelID string
	DiscordThreadID  string
	DiscordMessageID string
	DiscordUserID    string
	XAccountID       string
	XConversationID  string
	XReplyToTweetID  string
	XUserID          string
	XUsername        string
}

type ChannelTaskRunner func(context.Context, ChannelTaskRunRequest)
