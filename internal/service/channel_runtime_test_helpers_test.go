package service

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/persistence"
)

// These adapters keep test setup concise; production uses the full runtime entry points.

func newChannelActionSummaryCollector() *channelActionSummaryCollector {
	return &channelActionSummaryCollector{
		createdLines: []string{},
		editedLines:  []string{},
	}
}

func (s *DiscordService) buildDiscordActionToolRuntime(projectID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildDiscordActionToolRuntimeForTask(projectID, "", actionCtx, collector)
}

func (s *DiscordService) discordActionHandlers(projectID string, actionCtx discordActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.discordActionHandlersForTask(projectID, "", actionCtx, collector)
}

func (s *SlackService) buildSlackActionToolRuntime(projectID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildSlackActionToolRuntimeForTask(projectID, "", actionCtx, collector)
}

func (s *SlackService) slackActionHandlers(projectID string, actionCtx slackActionContext, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.slackActionHandlersForTask(projectID, "", actionCtx, collector)
}

func (s *TelegramService) buildTelegramActionToolRuntime(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) *llmcontracts.RuntimeTools {
	return s.buildTelegramActionToolRuntimeForTask(projectID, "", chatID, userID, collector)
}

func (s *TelegramService) telegramActionHandlers(projectID string, chatID int64, userID int64, collector *channelActionSummaryCollector) map[string]chatcontrol.RuntimeActionHandler {
	return s.telegramActionHandlersForTask(projectID, "", chatID, userID, collector)
}

// handleChatMessage forwards a Telegram message to the chat orchestrator.
func (s *TelegramService) handleChatMessage(message *tgbotapi.Message) {
	s.handleChatMessageWithDurableHandoff(context.Background(), message, nil)
}

func (s *TelegramService) cachedTelegramActiveProject(userID int64) (string, bool, uint64) {
	projectID, ok, version, _ := s.cachedTelegramActiveProjectWithGeneration(userID)
	return projectID, ok, version
}

func (s *EmailService) resolveAuthorizedProject(ctx context.Context, sender string) string {
	projectID, _, _ := s.resolveAuthorizedProjectForInbound(ctx, sender)
	return projectID
}

func isIgnoredEmail(msg EmailInboundMessage, selfAddress string) bool {
	return isIgnoredEmailWithNormalizedSelfAddress(msg, persistence.NormalizeEmailAddress(selfAddress))
}
