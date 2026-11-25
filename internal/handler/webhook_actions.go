package handler

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"net/http"
)

type webhookMutationArgs struct {
	ProjectID, WebhookID string
	WebhookIDs           []string
	Form                 webhookEndpointForm
}

func ownWebhookMutation(h *Handler, ctx context.Context, a webhookMutationArgs) (string, authz.Permission, error) {
	if h.webhookRepo == nil {
		return "", "", statusErr(http.StatusServiceUnavailable, "webhook repository not configured")
	}
	return a.ProjectID, authz.Edit, nil
}

var actWebhookCreate = defineAction("webhook.create", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (*models.WebhookEndpoint, error) {
	projectID, form := a.ProjectID, a.Form
	if err := h.validateWebhookAgentIDs(ctx, projectID, form.AgentIDs); err != nil {
		return nil, statusErr(http.StatusBadRequest, "invalid webhook agent assignment")
	}
	w := &models.WebhookEndpoint{ProjectID: projectID}
	applyWebhookEndpointForm(w, form)
	if w.Name == "" {
		w.Name = "New Webhook"
	}

	if err := h.webhookRepo.Create(ctx, w); err != nil {
		return nil, statusErr(http.StatusInternalServerError, "failed to create webhook: "+err.Error())
	}

	// Save agent assignments
	if len(form.AgentIDs) > 0 {
		if err := h.webhookRepo.SetEndpointAgents(ctx, w.ID, form.AgentIDs); err != nil {
			return nil, statusErr(http.StatusInternalServerError, "failed to save webhook agents: "+err.Error())
		}
	}

	return w, nil

})

var actWebhookUpdate = defineAction("webhook.update", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (*models.WebhookEndpoint, error) {
	id, projectID, form := a.WebhookID, a.ProjectID, a.Form
	w, err := h.webhookRepo.GetByIDForProject(ctx, id, projectID)
	if err != nil {
		return nil, statusErr(http.StatusInternalServerError, "failed to load webhook")
	}
	if w == nil {
		return nil, statusErr(http.StatusNotFound, "webhook not found")
	}

	if err := h.validateWebhookAgentIDs(ctx, w.ProjectID, form.AgentIDs); err != nil {
		return nil, statusErr(http.StatusBadRequest, "invalid webhook agent assignment")
	}
	applyWebhookEndpointForm(w, form)

	if err := h.webhookRepo.Update(ctx, w); err != nil {
		return nil, statusErr(http.StatusInternalServerError, "failed to update webhook")
	}

	// Update agent assignments
	if err := h.webhookRepo.SetEndpointAgents(ctx, w.ID, form.AgentIDs); err != nil {
		return nil, statusErr(http.StatusInternalServerError, "failed to save webhook agents: "+err.Error())
	}

	return w, nil

})

var actWebhookTest = defineAction("webhook.test", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (*models.Task, error) {
	id, projectID := a.WebhookID, a.ProjectID
	endpoint, err := h.webhookRepo.GetByIDForProject(ctx, id, projectID)
	if err != nil || endpoint == nil {
		return nil, statusErr(http.StatusNotFound, "webhook not found")
	}

	// Create a synthetic test task
	testPayload := `{"event_type":"test","summary":"Test webhook event","source":"openvibely_test"}`
	task, err := h.createWebhookTaskFromEndpoint(ctx, endpoint, "test", "Test webhook event", testPayload)
	if err != nil {
		return nil, statusErr(http.StatusInternalServerError, "failed to create test task: "+err.Error())
	}

	return task, nil

})

var actWebhookDelete = defineAction("webhook.delete", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (struct{}, error) {
	err := h.webhookRepo.DeleteForProject(ctx, a.ProjectID, a.WebhookID)
	if errors.Is(err, persistence.ErrWebhookNotFound) {
		return struct{}{}, statusErr(http.StatusNotFound, "webhook not found")
	}
	return struct{}{}, err
})
var actWebhookDeleteBulk = defineAction("webhook.delete_bulk", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (struct{}, error) {
	if err := h.webhookRepo.DeleteBulk(ctx, a.ProjectID, a.WebhookIDs); err != nil {
		return struct{}{}, statusErr(http.StatusBadRequest, "all selected webhooks must belong to the current project")
	}
	return struct{}{}, nil
})

// Rotation intentionally returns the newly generated signing secret to the
// authorized editor; ordinary webhook reads continue to scrub credentials.
var actWebhookRotate = defineAction("webhook.rotate", ownWebhookMutation, func(h *Handler, ctx context.Context, a webhookMutationArgs) (string, error) {
	endpoint, err := h.webhookRepo.GetByIDForProject(ctx, a.WebhookID, a.ProjectID)
	if err != nil {
		return "", err
	}
	if endpoint == nil {
		return "", statusErr(http.StatusNotFound, "webhook not found")
	}
	secret, err := h.webhookRepo.RotateSecret(ctx, a.WebhookID)
	if errors.Is(err, persistence.ErrWebhookNotFound) {
		return "", statusErr(http.StatusNotFound, "webhook not found")
	}
	return secret, err
})
