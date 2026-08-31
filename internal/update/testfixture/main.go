package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/openvibely/openvibely/internal/update"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == update.ExecutableUpdateHelperCommand {
		runExecutableUpdateHelper()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == update.AppBundleUpdateHelperCommand {
		runAppBundleUpdateHelper()
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "serve" || os.Args[2] != "--listen" {
		fatal(fmt.Errorf("usage: %s serve --listen host:port", os.Args[0]))
	}
	server := &http.Server{Addr: os.Args[3]}
	mux := http.NewServeMux()
	healthChecksRemaining := exitAfterHealthChecks()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		reportedVersion := version
		if override := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_HEALTH_VERSION"); override != "" {
			reportedVersion = override
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "version": reportedVersion, "actual_version": version})
		if healthChecksRemaining != nil {
			*healthChecksRemaining = *healthChecksRemaining - 1
			if *healthChecksRemaining <= 0 {
				go func() { _ = server.Shutdown(context.Background()) }()
			}
		}
	})
	mux.HandleFunc("/apply", func(w http.ResponseWriter, r *http.Request) {
		staged := update.LocalStagedUpdate{
			ArtifactPath:    os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_STAGED"),
			InstallPath:     os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_CURRENT"),
			BackupPath:      os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_BACKUP"),
			Version:         "0.6.0",
			PreviousVersion: "0.5.0",
			OutcomeID:       os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_OUTCOME_ID"),
		}
		workingDirectory, err := os.Getwd()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		installer := &update.BinaryInstaller{
			HealthURL:        "http://" + os.Args[3] + "/health",
			Arguments:        append([]string(nil), os.Args...),
			WorkingDirectory: workingDirectory,
			StartHelper:      func(cmd *exec.Cmd) error { return cmd.Start() },
			Shutdown: func() {
				go func() { _ = server.Shutdown(context.Background()) }()
			},
		}
		if err := installer.Apply(r.Context(), staged); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		installer.ShutdownForRestart()
	})
	server.Handler = mux
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stopping
		_ = server.Shutdown(context.Background())
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fatal(err)
	}
}

func exitAfterHealthChecks() *int {
	value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_EXIT_AFTER_HEALTH")
	if value == "" {
		return nil
	}
	count, err := strconv.Atoi(value)
	if err != nil {
		fatal(err)
	}
	if count <= 0 {
		count = 1
	}
	return &count
}

func runExecutableUpdateHelper() {
	cfg, err := update.ParseExecutableUpdateHelperArgs(os.Args[2:])
	if err == nil {
		if cfg.RelaunchMetadataPath != "" {
			err = update.LoadExecutableUpdateHelperRelaunchFile(cfg.RelaunchMetadataPath, &cfg)
		} else {
			err = update.LoadExecutableUpdateHelperRelaunch(os.Stdin, &cfg)
		}
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS"); value != "" {
		milliseconds, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			fatal(parseErr)
		}
		cfg.WaitTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS"); value != "" {
		milliseconds, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			fatal(parseErr)
		}
		cfg.ValidationTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if err == nil {
		err = update.RunExecutableUpdateHelper(context.Background(), cfg)
	}
	if err != nil {
		fatal(err)
	}
}

func runAppBundleUpdateHelper() {
	cfg, err := update.ParseAppBundleUpdateHelperArgs(os.Args[2:])
	if err == nil {
		err = update.LoadAppBundleUpdateHelperRelaunch(os.Stdin, &cfg)
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS"); value != "" {
		milliseconds, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			fatal(parseErr)
		}
		cfg.WaitTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS"); value != "" {
		milliseconds, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			fatal(parseErr)
		}
		cfg.ValidationTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if err == nil {
		err = update.RunAppBundleUpdateHelper(context.Background(), cfg)
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
