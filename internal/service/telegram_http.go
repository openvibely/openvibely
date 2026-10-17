package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Wrap the HTTP client, not its transport: http.Client adds the credential-bearing
// URL to transport errors. Sanitize before the Telegram library or callers see it.
type telegramHTTPClient struct {
	client tgbotapi.HTTPClient
	token  string
}

func newSafeTelegramBot(token string) (*tgbotapi.BotAPI, error) {
	return tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint,
		telegramHTTPClient{client: &http.Client{}, token: token})
}

func (c telegramHTTPClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.client.Do(req)
	if err == nil {
		return resp, nil
	}
	operation := "request"
	if strings.HasPrefix(req.URL.Path, "/file/bot") {
		operation = "downloadFile"
	} else if _, method, ok := strings.Cut(strings.TrimPrefix(req.URL.Path, "/"), "/"); ok && method != "" && !strings.Contains(method, "/") {
		operation = method
	}
	return resp, safeTelegramHTTPError(operation, c.token, err)
}

type telegramRequestError struct {
	message string
	cause   error
}

func (e *telegramRequestError) Error() string { return e.message }

// Preserve errors.Is checks without exposing the unsafe error through Unwrap.
func (e *telegramRequestError) Is(target error) bool { return errors.Is(e.cause, target) }

func safeTelegramHTTPError(operation, token string, err error) error {
	cause := err
	for {
		var urlErr *url.Error
		if !errors.As(cause, &urlErr) {
			break
		}
		cause = urlErr.Err
	}
	message := fmt.Sprintf("telegram %s: %s", operation, cause.Error())
	// Custom transports may repeat credentials in the underlying error text.
	if token != "" {
		for _, secret := range []string{token, url.PathEscape(token), url.QueryEscape(token)} {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	return &telegramRequestError{message: message, cause: err}
}
