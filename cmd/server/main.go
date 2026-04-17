package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/server"
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
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := server.Start(ctx, cfg)
	if err != nil {
		log.Fatalf("failed to start server: %v", err)
	}

	// Wait for termination signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	inst.Shutdown()
}
