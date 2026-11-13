//go:build linux

package main

import (
	"testing"
	"time"
)

func TestDesktopIdleProtectsRequestsAndNever(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		age, timeOut time.Duration
		requests     int
		want         bool
	}{
		{"expired", 31 * time.Minute, 30 * time.Minute, 0, true},
		{"recent", 29 * time.Minute, 30 * time.Minute, 0, false},
		{"viewer", time.Hour, 30 * time.Minute, 1, false},
		{"tool", time.Hour, 30 * time.Minute, 2, false},
		{"never", time.Hour, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &desktopProcess{lastActivity: now.Add(-tc.age), requests: tc.requests}
			if got := desktopIdleEligible(p, now, tc.timeOut); got != tc.want {
				t.Fatalf("eligible=%v want %v", got, tc.want)
			}
		})
	}
}
