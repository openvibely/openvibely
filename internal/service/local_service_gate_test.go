package service

import (
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalPausedChannelsDoNotStart(t *testing.T) {
	// Deliberately omit repositories and credentials: a paused startup or reload
	// must return before looking them up or making any network requests.
	gate := func() bool { return false }
	slack := &SlackService{}
	slack.SetStartGate(gate)
	require.NoError(t, slack.Start())
	require.NoError(t, slack.ReloadFromSettings(t.Context()))
	discord := &DiscordService{}
	discord.SetStartGate(gate)
	require.NoError(t, discord.Start())
	email := &EmailService{}
	email.SetStartGate(gate)
	require.NoError(t, email.Start())
	telegram := &TelegramService{}
	telegram.SetStartGate(gate)
	telegram.Start()
	require.False(t, telegram.IsRunning())
	x := &XService{}
	x.SetStartGate(gate)
	require.NoError(t, x.Start())
	require.NoError(t, x.StartVerified(XUser{ID: "test"}))
}

func TestLocalXServiceCanRestartAfterPause(t *testing.T) {
	_, svc, _, _, _, _, _ := setupXServiceTest(t)
	svc.SetAPI(&fakeXAPI{me: XUser{ID: "bot"}})
	require.NoError(t, svc.Start())
	svc.Stop()
	require.NoError(t, svc.Start())
	require.True(t, svc.Status().Running)
	require.NoError(t, svc.ctx.Err(), "restart must not reuse the canceled polling context")
	svc.Stop()
}

func TestLocalTelegramPauseDrainsPollBeforeResume(t *testing.T) {
	client := &telegramStubClient{blockUpdates: make(chan struct{})}
	bot, err := tgbotapi.NewBotAPIWithClient("test-token", tgbotapi.APIEndpoint, client)
	require.NoError(t, err)
	svc := &TelegramService{bot: bot}
	defer func() { client.unblock(); svc.StopAndWait() }()
	svc.Start()
	require.Eventually(t, func() bool { return atomic.LoadInt32(&client.activeUpdates) == 1 }, time.Second, time.Millisecond)
	done := make(chan struct{})
	go func() { svc.StopAndWait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel the active poll")
	}
	require.False(t, svc.IsRunning())
	require.EqualValues(t, 0, atomic.LoadInt32(&client.activeUpdates), "pause must wait for the cancelled poll to exit")
	// Leave the stub blocked: both stopping and restarting must work through
	// the polling context, without manually completing the old request.
	svc.Start()
	require.True(t, svc.IsRunning())
	require.Eventually(t, func() bool { return atomic.LoadInt32(&client.activeUpdates) == 1 }, time.Second, time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt32(&client.maxActiveUpdates))
}
