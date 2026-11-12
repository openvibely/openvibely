package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// openStartupBrowser stays in the CLI launcher so desktop startup is unaffected.
// The caller runs it asynchronously so a slow OS launcher cannot block shutdown.
func openStartupBrowser(ctx context.Context, baseURL, noBrowser string, open func(string) error) error {
	disabled, _ := strconv.ParseBool(strings.TrimSpace(noBrowser))
	if disabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/system/health", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := open(baseURL + "/chat"); err != nil {
					return fmt.Errorf("open browser: %w", err)
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
