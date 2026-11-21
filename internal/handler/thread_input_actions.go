package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type inputMutationArgs struct {
	InputID   string
	Op        string
	Content   string `remote:"raw"`
	EditToken string `remote:"raw"`
	Resume    bool
	Hold      bool
}
type inputMutationResult struct {
	Content string `remote:"raw"`
	Status  string
}

func inputOwner(h *Handler, ctx context.Context, id string) (string, authz.Permission, error) {
	if h.threadInputRepo == nil {
		return "", "", statusErr(http.StatusInternalServerError, "thread input queue is unavailable")
	}
	input, err := h.threadInputRepo.GetByID(ctx, id)
	if err != nil {
		return "", "", err
	}
	if input == nil {
		return "", "", statusErr(http.StatusNotFound, "input not found")
	}
	return input.ProjectID, authz.Edit, nil
}

var actInputMutation = defineAction("thread.input.mutate", func(h *Handler, ctx context.Context, a inputMutationArgs) (string, authz.Permission, error) {
	return inputOwner(h, ctx, a.InputID)
}, func(h *Handler, ctx context.Context, a inputMutationArgs) (inputMutationResult, error) {
	var input *models.ThreadInput
	var err error
	var conflict, failure string
	switch a.Op {
	case "edit":
		a.Content = strings.TrimSpace(a.Content)
		if a.Content == "" {
			return inputMutationResult{}, statusErr(http.StatusBadRequest, "Message cannot be empty")
		}
		input, err = h.threadInputRepo.EditPending(ctx, a.InputID, a.Content, a.EditToken)
		conflict, failure = "This message is no longer pending and cannot be edited", "Failed to edit message"
	case "cancel":
		input, err = h.threadInputRepo.CancelPending(ctx, a.InputID)
		if errors.Is(err, persistence.ErrInputNotPending) {
			return inputMutationResult{Status: "not_pending"}, nil
		}
		if err != nil {
			return inputMutationResult{}, statusErr(http.StatusInternalServerError, "failed to cancel queued input")
		}
		h.publishThreadInputCancelledEvent(input)
		return inputMutationResult{Status: "cancelled"}, nil
	case "renew":
		err = h.threadInputRepo.RenewEditHold(ctx, a.InputID, a.EditToken)
		conflict, failure = "Editing session expired. Copy your draft and reopen the editor.", "Failed to renew editing session"
	case "hold":
		input, err = h.threadInputRepo.SetEditHold(ctx, a.InputID, a.Hold, a.EditToken)
		conflict, failure = "This message is already being consumed", "Failed to hold message"
	default:
		return inputMutationResult{}, statusErr(http.StatusBadRequest, "invalid input action")
	}
	if errors.Is(err, persistence.ErrInputNotPending) {
		return inputMutationResult{}, statusErr(http.StatusConflict, conflict)
	}
	if err != nil {
		return inputMutationResult{}, statusErr(http.StatusInternalServerError, failure)
	}
	if input == nil {
		return inputMutationResult{}, nil
	}
	if a.Op == "edit" && a.Resume || a.Op == "hold" && !a.Hold {
		h.resumeEditedInput(input)
	}
	return inputMutationResult{Content: input.Content}, nil
})
