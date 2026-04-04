package handler

import (
	"context"
	"fmt"

	"github.com/openvibely/openvibely/internal/service"
)

type fakeSlackService struct {
	statusFn     func(ctx context.Context) (service.SlackConnectionStatus, error)
	connectURLFn func(ctx context.Context, redirectURI string) (string, error)
	callbackFn   func(ctx context.Context, code, state, redirectURI string) error
	disconnectFn func(ctx context.Context) error
	reloadFn     func(ctx context.Context) error
	testFn       func(ctx context.Context) error
}

func (f *fakeSlackService) GetConnectionStatus(ctx context.Context) (service.SlackConnectionStatus, error) {
	if f != nil && f.statusFn != nil {
		return f.statusFn(ctx)
	}
	return service.SlackConnectionStatus{}, nil
}

func (f *fakeSlackService) ConnectURL(ctx context.Context, redirectURI string) (string, error) {
	if f != nil && f.connectURLFn != nil {
		return f.connectURLFn(ctx, redirectURI)
	}
	return "", fmt.Errorf("slack connect url not configured")
}

func (f *fakeSlackService) HandleOAuthCallback(ctx context.Context, code, state, redirectURI string) error {
	if f != nil && f.callbackFn != nil {
		return f.callbackFn(ctx, code, state, redirectURI)
	}
	return nil
}

func (f *fakeSlackService) Disconnect(ctx context.Context) error {
	if f != nil && f.disconnectFn != nil {
		return f.disconnectFn(ctx)
	}
	return nil
}

func (f *fakeSlackService) ReloadFromSettings(ctx context.Context) error {
	if f != nil && f.reloadFn != nil {
		return f.reloadFn(ctx)
	}
	return nil
}

func (f *fakeSlackService) TestConnection(ctx context.Context) error {
	if f != nil && f.testFn != nil {
		return f.testFn(ctx)
	}
	return nil
}
