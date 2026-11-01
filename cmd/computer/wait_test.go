//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitReturnsFreshScreenWithoutReplayingInput(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	t.Setenv("ACTION_LOG", log)
	for name, body := range map[string]string{"import": "echo screenshot >> \"$ACTION_LOG\"\nprintf fresh-screen", "xdotool": "echo input >> \"$ACTION_LOG\""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	result, err := action(context.Background(), "wait", map[string]any{"duration_ms": float64(30)})
	if err != nil || result == nil {
		t.Fatalf("%v %v", result, err)
	}
	if time.Since(start) < 30*time.Millisecond {
		t.Fatal("did not wait")
	}
	data, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(data)) != "screenshot" {
		t.Fatalf("wait must only capture fresh screen: %s %v", data, err)
	}
	content := result.(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["data"] != "ZnJlc2gtc2NyZWVu" {
		t.Fatal("missing fresh image")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = action(ctx, "wait", map[string]any{"duration_ms": float64(10000)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(log)
	if string(after) != string(data) {
		t.Fatal("captured after cancellation")
	}
}
func TestWaitRejectsInvalidDuration(t *testing.T) {
	for _, v := range []any{nil, "50", float64(-1), float64(10001), 0.5} {
		if _, err := action(context.Background(), "wait", map[string]any{"duration_ms": v}); err == nil {
			t.Fatalf("accepted %v", v)
		}
	}
}
