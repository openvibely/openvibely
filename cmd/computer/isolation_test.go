//go:build linux

package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupervisorDeniesUnauthenticatedLoopback(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_TOKEN", "secret")
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		req := httptest.NewRequest("GET", "http://"+host+"/health", nil)
		req.RemoteAddr = "127.0.0.1:10000"
		res := httptest.NewRecorder()
		server().ServeHTTP(res, req)
		if res.Code != 403 {
			t.Fatalf("%s bypassed authentication: %d", host, res.Code)
		}
	}
}
func TestTaskAndShellEnvironmentsExcludeControlCredentials(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_TOKEN", "never-inherit")
	env := strings.Join(taskEnvironment("task-test"), "\n")
	if strings.Contains(env, "never-inherit") || strings.Contains(env, "COMPUTER_TOKEN") {
		t.Fatal("task inherited router credentials")
	}
	t.Setenv("OPENVIBELY_ISOLATED", "1")
	t.Setenv("OPENVIBELY_SHELL_UID", "20001")
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-test")
	cmd, err := shellCommand(context.Background(), "true", "/")
	if err != nil {
		t.Fatal(err)
	}
	env = strings.Join(cmd.Env, "\n")
	for _, secret := range []string{"COMPUTER_TOKEN", "XAUTHORITY", "DISPLAY=", "DESKTOP_UID", "never-inherit"} {
		if strings.Contains(env, secret) {
			t.Fatalf("shell inherited %s", secret)
		}
	}
	args := strings.Join(cmd.Args, " ")
	for _, flag := range []string{"--reuid=20001", "--clear-groups", "--bounding-set=-all", "--no-new-privs"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %s", flag)
		}
	}
}
func TestShellFailsClosedWithoutIsolation(t *testing.T) {
	t.Setenv("OPENVIBELY_ISOLATED", "")
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-test")
	if _, err := shellCommand(context.Background(), "true", "/"); err == nil {
		t.Fatal("unisolated task shell accepted")
	}
	t.Setenv("OPENVIBELY_ISOLATED", "1")
	t.Setenv("OPENVIBELY_SHELL_UID", "0")
	if _, err := shellCommand(context.Background(), "true", "/"); err == nil {
		t.Fatal("root shell accepted")
	}
}
