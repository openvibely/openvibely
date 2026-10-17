package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestTelegramHTTPFailuresOmitCredentials(t *testing.T) {
	const token = "123456:secret-test-token"
	for _, operation := range []string{"getMe", "getUpdates", "sendMessage", "editMessageText", "getFile", "downloadFile"} {
		t.Run(operation, func(t *testing.T) {
			client := telegramHTTPClient{token: token, client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			})}}
			var err error
			if operation == "downloadFile" {
				req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/file/bot"+token+"/documents/private.txt", nil)
				_, err = client.Do(req)
			} else {
				bot := &tgbotapi.BotAPI{Token: token, Client: client}
				bot.SetAPIEndpoint(tgbotapi.APIEndpoint)
				_, err = bot.MakeRequest(operation, tgbotapi.Params{})
			}
			if err == nil || !strings.Contains(err.Error(), operation) || !strings.Contains(err.Error(), "deadline exceeded") {
				t.Fatalf("missing useful error context: %v", err)
			}
			for _, forbidden := range []string{token, "https://", "private.txt"} {
				if strings.Contains(fmt.Sprintf("%+v", err), forbidden) {
					t.Fatalf("unsafe error: %v", err)
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("deadline identity lost")
			}
			var unsafe *url.Error
			if errors.As(err, &unsafe) {
				t.Fatal("credential-bearing error exposed through chain")
			}
		})
	}
}

func TestSafeTelegramBotInitialization(t *testing.T) {
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset by peer")
	})
	_, err := newSafeTelegramBot("123:initialization-secret")
	if err == nil || strings.Contains(err.Error(), "initialization-secret") || !strings.Contains(err.Error(), "getMe") {
		t.Fatalf("unsafe or missing initialization error: %v", err)
	}
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":123,"is_bot":true,"first_name":"Test"}}`))}, nil
	})
	bot, err := newSafeTelegramBot("123:initialization-secret")
	if err != nil || bot.Self.ID != 123 {
		t.Fatalf("successful initialization failed: %v", err)
	}
}

func TestSafeTelegramHTTPErrorRedactsNestedCredentials(t *testing.T) {
	const token = "123:secret"
	err := safeTelegramHTTPError("getUpdates", token, &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + token + "/getUpdates", Err: fmt.Errorf("transport echoed %s and %s", token, url.QueryEscape(token))})
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), url.QueryEscape(token)) {
		t.Fatalf("nested credential leaked: %v", err)
	}
}
