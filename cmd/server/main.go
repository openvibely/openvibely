package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/server"
	"github.com/openvibely/openvibely/internal/update"
)

// @title OpenVibely API
// @version 1.0
// @description REST API for OpenVibely - AI-powered task scheduling and management
// @description This API provides endpoints for managing projects, tasks, and chat interactions with AI agents.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url https://github.com/openvibely/openvibely
// @contact.email support@openvibely.dev

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:3001
// @BasePath /
// @schemes http https

// @tag.name projects
// @tag.description Operations for managing projects

// @tag.name chat
// @tag.description AI chat operations with file upload support

// @tag.name analytics
// @tag.description Execution analytics API endpoints

// @tag.name capacity
// @tag.description Worker capacity and utilization API endpoints

// @tag.name workflows
// @tag.description Multi-agent workflow and metrics API endpoints

// @tag.name autonomous
// @tag.description Autonomous build and trend intelligence API endpoints

// @tag.name collisions
// @tag.description Semantic collision analysis API endpoints

func main() {
	if len(os.Args) > 1 && os.Args[1] == "update-helper" {
		cfg, err := update.ParseBinaryHelperArgs(os.Args[2:])
		if err == nil {
			err = update.LoadBinaryHelperRelaunch(os.Stdin, &cfg)
		}
		if err == nil {
			err = update.RunBinaryHelper(context.Background(), cfg)
		}
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "3001"
		}
		endpoint := "http://127.0.0.1:" + port + "/api/system/health"
		if err := runHealthcheck(endpoint, &http.Client{Timeout: 5 * time.Second}); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := server.Start(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to start server: %v", err)
	}

	// Wait for termination signal or an update helper handoff.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	waitForShutdown(sigCh, inst.ShutdownRequested)

	inst.Shutdown()
}

func waitForShutdown(signals <-chan os.Signal, requested <-chan struct{}) {
	select {
	case <-signals:
	case <-requested:
	}
}

func runHealthcheck(endpoint string, client *http.Client) error {
	resp, err := client.Get(endpoint)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned HTTP %d", resp.StatusCode)
	}
	return nil
}
