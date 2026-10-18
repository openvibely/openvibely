package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestChannelDownloadsUseGeneralRetries(t *testing.T) {
	for _, channel := range []string{"telegram", "slack", "discord"} {
		t.Run(channel, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if channel == "slack" && req.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing download authentication")
				}
				status := http.StatusServiceUnavailable
				if calls == 2 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("image bytes")), Request: req}, nil
			})}
			var output bytes.Buffer
			var err error
			switch channel {
			case "telegram":
				req, _ := http.NewRequest(http.MethodGet, "https://api.telegram.org/file/botfixture/image.png", nil)
				var resp *http.Response
				resp, err = (telegramHTTPClient{client: client, token: "fixture"}).Do(req)
				if err == nil {
					_, err = io.Copy(&output, resp.Body)
					resp.Body.Close()
				}
			case "slack":
				svc := &SlackService{httpClient: client}
				err = svc.downloadSlackPrivateFile(context.Background(), "fixture", "https://files.slack.com/files-pri/T-F/image.png", &output)
			case "discord":
				svc := &DiscordService{httpClient: client}
				err = svc.downloadDiscordFile(context.Background(), "https://cdn.discordapp.com/attachments/image.png", &output)
			}
			if err != nil || calls != 2 || output.String() != "image bytes" {
				t.Fatalf("calls=%d output=%q err=%v", calls, output.String(), err)
			}
		})
	}
}
