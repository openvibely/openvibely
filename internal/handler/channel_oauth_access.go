package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type channelOAuthFlow struct {
	provider, userID string
	expires          time.Time
}

// Pending connection state is single-use and process-local: restart requires a
// new connection attempt, never a fallback to the callback browser's identity.
func (h *Handler) bindChannelOAuth(c echo.Context, provider, destination string) (string, error) {
	if h.projectAccessSvc == nil {
		return destination, nil
	}
	if err := h.requireCurrentOrgAdmin(c); err != nil {
		return "", err
	}
	actor, _ := models.PrincipalFromContext(c.Request().Context())
	u, err := url.Parse(destination)
	if err != nil {
		return "", err
	}
	q := u.Query()
	state := q.Get("state")
	if state == "" {
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		state = hex.EncodeToString(nonce[:])
		q.Set("state", state)
		u.RawQuery = q.Encode()
	}
	h.channelOAuthMu.Lock()
	defer h.channelOAuthMu.Unlock()
	if h.channelOAuthFlows == nil {
		h.channelOAuthFlows = make(map[string]channelOAuthFlow)
	}
	now := time.Now()
	for key, flow := range h.channelOAuthFlows {
		if !now.Before(flow.expires) {
			delete(h.channelOAuthFlows, key)
		}
	}
	if _, exists := h.channelOAuthFlows[state]; exists {
		return "", authz.ErrDenied
	}
	if len(h.channelOAuthFlows) >= 256 {
		return "", authz.ErrDenied
	}
	h.channelOAuthFlows[state] = channelOAuthFlow{provider: provider, userID: actor.UserID, expires: now.Add(10 * time.Minute)}
	return u.String(), nil
}

func (h *Handler) consumeChannelOAuth(c echo.Context, provider string) error {
	if h.projectAccessSvc == nil {
		return nil
	}
	state := c.QueryParam("state")
	h.channelOAuthMu.Lock()
	flow, ok := h.channelOAuthFlows[state]
	if ok {
		delete(h.channelOAuthFlows, state)
	}
	h.channelOAuthMu.Unlock()
	if !ok || flow.provider != provider || !time.Now().Before(flow.expires) {
		return authz.ErrDenied
	}
	if err := h.requireOAuthActor(c.Request().Context(), flow.userID); err != nil {
		return err
	}
	c.SetRequest(c.Request().WithContext(oauthActorContext(c.Request().Context(), flow.userID)))
	return nil
}
