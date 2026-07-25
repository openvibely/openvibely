package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const automationConfirmationTTL = 30 * time.Minute
const automationConfirmationSecretSetting = "automation_confirmation_secret"

func LoadOrCreateAutomationConfirmationSecret(ctx context.Context, settings *repository.SettingsRepo) ([]byte, error) {
	if settings == nil {
		return nil, errors.New("settings repository is unavailable")
	}
	stored, err := settings.Get(ctx, automationConfirmationSecretSetting)
	if err == nil && strings.TrimSpace(stored) != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(stored)
		if decodeErr == nil && len(decoded) >= 32 {
			return decoded, nil
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	if err := settings.Set(ctx, automationConfirmationSecretSetting, base64.RawURLEncoding.EncodeToString(secret)); err != nil {
		return nil, err
	}
	return secret, nil
}

type AutomationConfirmationService struct {
	repo     *repository.AutomationRepo
	execRepo *repository.ExecutionRepo
	secret   []byte
	now      func() time.Time
}

type AutomationConfirmationIssue struct {
	ProjectID      string
	PrincipalID    string
	ThreadID       string
	PlanMessageID  string
	AutomationName string
	Source         string
	Candidate      models.AutomationDraftCandidate
}

type AutomationChatConfirmationContext struct {
	Token                 string
	TokenID               string
	ConfirmingUserInputID string
	AutomationName        string
	Source                string
	Candidate             models.AutomationDraftCandidate
}

func (s *AutomationConfirmationService) PrepareChatConfirmation(ctx context.Context, projectID, principalID, threadID, inputID, message string) (*AutomationChatConfirmationContext, error) {
	if s == nil || s.repo == nil || s.execRepo == nil || len(s.secret) < 16 {
		return nil, errors.New("automation confirmation service is unavailable")
	}
	now := s.now().UTC()
	receipt, automationName, err := s.repo.GetPendingAutomationConfirmation(ctx, projectID, principalID, threadID, now)
	if err != nil || receipt == nil {
		return nil, err
	}
	if normalizeAutomationSaveCommand(message) != normalizeAutomationSaveCommand("save "+automationName) {
		return nil, nil
	}
	inputExecution, err := s.execRepo.GetByID(ctx, inputID)
	if err != nil || inputExecution == nil || inputExecution.TaskID != threadID {
		return nil, errors.New("confirming user input not found in the pending plan thread")
	}
	planExecution, err := s.execRepo.GetByID(ctx, receipt.PlanMessageID)
	if err != nil || planExecution == nil || planExecution.Status != models.ExecCompleted || !inputExecution.StartedAt.After(planExecution.StartedAt) {
		return nil, errors.New("automation save requires a completed plan and later user input")
	}
	marker := repository.AutomationConfirmationInputMarker{InputID: inputID, TokenID: receipt.TokenID,
		ProjectID: projectID, PrincipalID: principalID, ThreadID: threadID, Method: "command"}
	if err := s.repo.MarkAutomationConfirmationInput(ctx, marker); err != nil {
		return nil, err
	}
	return &AutomationChatConfirmationContext{Token: s.signToken(receipt.TokenID), TokenID: receipt.TokenID,
		ConfirmingUserInputID: inputID, AutomationName: automationName, Source: receipt.Source}, nil
}

func (s *AutomationConfirmationService) ResolveChatConfirmation(ctx context.Context, token, projectID, principalID, threadID string) (*AutomationChatConfirmationContext, error) {
	if s == nil || s.repo == nil || len(s.secret) < 16 {
		return nil, errors.New("automation confirmation service is unavailable")
	}
	tokenID, err := s.verifyToken(token)
	if err != nil {
		return nil, err
	}
	receipt, err := s.repo.GetAutomationConfirmationReceipt(ctx, tokenID)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, errors.New("automation confirmation receipt not found")
	}
	if receipt.ProjectID != projectID || receipt.PrincipalID != principalID || receipt.ThreadID != threadID {
		return nil, errors.New("automation confirmation receipt scope does not match")
	}
	if !s.now().UTC().Before(receipt.ExpiresAt) {
		return nil, errors.New("automation confirmation receipt expired")
	}
	var candidate models.AutomationDraftCandidate
	if err := json.Unmarshal([]byte(receipt.CandidateJSON), &candidate); err != nil {
		return nil, errors.New("pending Automation save is invalid")
	}
	return &AutomationChatConfirmationContext{Token: token, TokenID: tokenID, AutomationName: receipt.AutomationName,
		Source: receipt.Source, Candidate: candidate}, nil
}

type AutomationChatConfirmation struct {
	Token                 string
	ProjectID             string
	PrincipalID           string
	ThreadID              string
	ConfirmingUserInputID string
	AutomationName        string
}

func NewAutomationConfirmationService(repo *repository.AutomationRepo, execRepo *repository.ExecutionRepo, secret []byte) *AutomationConfirmationService {
	return &AutomationConfirmationService{repo: repo, execRepo: execRepo, secret: append([]byte(nil), secret...), now: time.Now}
}

func (s *AutomationConfirmationService) Issue(ctx context.Context, input AutomationConfirmationIssue) (string, error) {
	if s == nil || s.repo == nil || len(s.secret) < 16 {
		return "", errors.New("automation confirmation service is unavailable")
	}
	if strings.TrimSpace(input.ProjectID) == "" || strings.TrimSpace(input.PrincipalID) == "" || strings.TrimSpace(input.ThreadID) == "" || strings.TrimSpace(input.PlanMessageID) == "" || strings.TrimSpace(input.AutomationName) == "" || strings.TrimSpace(input.Source) == "" {
		return "", errors.New("automation confirmation receipt scope is incomplete")
	}
	candidateJSON, err := json.Marshal(input.Candidate)
	if err != nil {
		return "", err
	}
	tokenID := repository.NewID()
	now := s.now().UTC()
	receipt := &models.AutomationChatConfirmationReceipt{TokenID: tokenID, ProjectID: input.ProjectID,
		PrincipalID: input.PrincipalID, ThreadID: input.ThreadID, PlanMessageID: input.PlanMessageID,
		AutomationName: input.AutomationName, Source: input.Source, CandidateJSON: string(candidateJSON),
		ExpiresAt: now.Add(automationConfirmationTTL)}
	if err := s.repo.CreateAutomationConfirmationReceipt(ctx, receipt); err != nil {
		return "", err
	}
	return s.signToken(tokenID), nil
}

func (s *AutomationConfirmationService) ValidateChatConfirmation(ctx context.Context, input AutomationChatConfirmation) (tokenID string, returnErr error) {
	defer func() {
		if returnErr != nil {
			automationobs.Event("automation.confirmation.rejected",
				automationobs.String("project_id", input.ProjectID), automationobs.String("thread_id", input.ThreadID),
				automationobs.String("confirming_input_id", input.ConfirmingUserInputID))
		}
	}()
	if s == nil || s.repo == nil || s.execRepo == nil || len(s.secret) < 16 {
		return "", errors.New("automation confirmation service is unavailable")
	}
	tokenID, err := s.verifyToken(input.Token)
	if err != nil {
		return "", err
	}
	receipt, err := s.repo.GetAutomationConfirmationReceipt(ctx, tokenID)
	if err != nil {
		return "", err
	}
	if receipt == nil {
		return "", errors.New("automation confirmation receipt not found")
	}
	if receipt.ProjectID != input.ProjectID || receipt.PrincipalID != input.PrincipalID || receipt.ThreadID != input.ThreadID {
		return "", errors.New("automation confirmation receipt scope does not match")
	}
	now := s.now().UTC()
	if !now.Before(receipt.ExpiresAt) {
		return "", errors.New("automation confirmation receipt expired")
	}
	if receipt.ConsumedAt != nil {
		return "", errors.New("automation confirmation receipt was already used")
	}
	if input.ConfirmingUserInputID == receipt.PlanMessageID {
		return "", errors.New("automation save requires a later user input")
	}
	planExecution, err := s.execRepo.GetByID(ctx, receipt.PlanMessageID)
	if err != nil || planExecution == nil {
		return "", errors.New("stored automation plan message not found")
	}
	confirmingExecution, err := s.execRepo.GetByID(ctx, input.ConfirmingUserInputID)
	if err != nil || confirmingExecution == nil {
		return "", errors.New("confirming user input not found")
	}
	if planExecution.TaskID != input.ThreadID || confirmingExecution.TaskID != input.ThreadID || !confirmingExecution.StartedAt.After(planExecution.StartedAt) {
		return "", errors.New("automation save requires a later user input in the same thread")
	}
	expected := normalizeAutomationSaveCommand("save " + input.AutomationName)
	if normalizeAutomationSaveCommand(confirmingExecution.PromptSent) != expected {
		return "", fmt.Errorf("automation save requires the exact confirmation command %q", "save "+input.AutomationName)
	}
	marked, err := s.repo.HasAutomationConfirmationInput(ctx, repository.AutomationConfirmationInputMarker{
		InputID: input.ConfirmingUserInputID, TokenID: tokenID, ProjectID: input.ProjectID,
		PrincipalID: input.PrincipalID, ThreadID: input.ThreadID, Method: "command",
	})
	if err != nil {
		return "", err
	}
	if !marked {
		return "", errors.New("confirming user input was not marked affirmative by the Chat host")
	}
	return tokenID, nil
}

func normalizeAutomationSaveCommand(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func (s *AutomationConfirmationService) signToken(tokenID string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(tokenID))
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *AutomationConfirmationService) verifyToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid automation confirmation token")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("invalid automation confirmation token")
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", errors.New("invalid automation confirmation token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(payload) == 0 {
		return "", errors.New("invalid automation confirmation token")
	}
	return string(payload), nil
}
