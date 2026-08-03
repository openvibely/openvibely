package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

type DockerCapabilities struct {
	SchemaVersion  int  `json:"schema_version"`
	ManagedUpdates bool `json:"managed_updates"`
}
type DockerRequest struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Status        string `json:"status"`
}
type DockerRequestStatus struct {
	SchemaVersion  int    `json:"schema_version"`
	RequestID      string `json:"request_id"`
	Status         string `json:"status"`
	CurrentVersion string `json:"current_version"`
	TargetVersion  string `json:"target_version"`
	FailureCode    string `json:"failure_code"`
}
type dockerPersistentState struct {
	RequestID              string `json:"request_id"`
	Status                 string `json:"status"`
	CurrentVersion         string `json:"current_version"`
	TargetVersion          string `json:"target_version"`
	DrainGeneration        string `json:"drain_generation"`
	CreateIdempotencyKey   string `json:"create_idempotency_key,omitempty"`
	CancelIdempotencyKey   string `json:"cancel_idempotency_key,omitempty"`
	ReportedCurrentVersion string `json:"reported_current_version,omitempty"`
	ReportedTargetVersion  string `json:"reported_target_version,omitempty"`
}

type DockerAgentInstaller struct {
	mu           sync.Mutex
	API          *AgentHTTPClient
	Client       *Client
	Current      CurrentBuild
	Drain        *DrainManager
	StatePath    string
	PollInterval time.Duration
	stateWriter  func(string, []byte) error
	state        dockerPersistentState
}

func (i *DockerAgentInstaller) Capabilities(ctx context.Context) (DockerCapabilities, error) {
	var result DockerCapabilities
	err := i.API.Do(ctx, http.MethodGet, "/v1/capabilities", "", nil, &result, http.StatusOK)
	if err == nil && result.SchemaVersion != 1 {
		return result, errors.New("unsupported Docker agent capabilities schema")
	}
	return result, err
}
func (i *DockerAgentInstaller) Stage(ctx context.Context, release VerifiedRelease) (any, error) {
	capabilities, err := i.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !capabilities.ManagedUpdates {
		return nil, errors.New("Docker agent does not support managed updates")
	}
	if release.Target.Kind != "oci" || !validOCIImageRef(release.Target.ImageRef) {
		return nil, errors.New("Docker update requires a signed OCI digest target")
	}
	if err := i.Client.ValidateForInstall(release, i.Current); err != nil {
		return nil, err
	}
	return release, nil
}
func (i *DockerAgentInstaller) Apply(ctx context.Context, value any) error {
	release, ok := value.(VerifiedRelease)
	if !ok {
		return errors.New("invalid Docker staged update")
	}
	if err := i.Client.ValidateForInstall(release, i.Current); err != nil {
		return err
	}
	status := i.Drain.Status()
	if status.State != DrainStateReady || status.Active.Total() != 0 {
		return errors.New("Docker update request requires an idle ready drain")
	}
	i.mu.Lock()
	i.state = dockerPersistentState{
		Status:               "creating",
		CurrentVersion:       i.Current.Version,
		TargetVersion:        release.Metadata.Version,
		DrainGeneration:      status.Generation,
		CreateIdempotencyKey: randomGeneration(),
	}
	i.mu.Unlock()
	if err := i.save(); err != nil {
		return err
	}
	if err := i.createRequest(ctx); err != nil {
		return err
	}
	return i.poll(ctx)
}

func (i *DockerAgentInstaller) createRequest(ctx context.Context) error {
	i.mu.Lock()
	state := i.state
	i.mu.Unlock()
	if state.RequestID != "" {
		return nil
	}
	if state.Status != "creating" || state.CreateIdempotencyKey == "" || state.TargetVersion == "" || state.CurrentVersion == "" || state.DrainGeneration == "" {
		return errors.New("Docker update request creation state is unavailable")
	}
	body := struct {
		SchemaVersion    int    `json:"schema_version"`
		RequestedVersion string `json:"requested_version"`
		CurrentVersion   string `json:"current_version"`
		DrainGeneration  string `json:"drain_generation"`
		ActiveTotal      int    `json:"active_total"`
	}{1, state.TargetVersion, state.CurrentVersion, state.DrainGeneration, 0}
	var created DockerRequest
	if err := i.API.Do(ctx, http.MethodPost, "/v1/update-requests", state.CreateIdempotencyKey, body, &created, http.StatusAccepted); err != nil {
		return fmt.Errorf("%w: create Docker update request: %v", ErrUpdateRecoveryPending, err)
	}
	if created.SchemaVersion != 1 || created.RequestID == "" || created.Status != "accepted" {
		return fmt.Errorf("%w: invalid Docker agent create response", ErrUpdateRecoveryPending)
	}
	i.mu.Lock()
	i.state.RequestID = created.RequestID
	i.state.Status = created.Status
	i.mu.Unlock()
	if err := i.save(); err != nil {
		i.mu.Lock()
		i.state.RequestID = ""
		i.state.Status = "creating"
		i.mu.Unlock()
		return fmt.Errorf("%w: persist accepted Docker update request: %v", ErrUpdateRecoveryPending, err)
	}
	return nil
}
func (i *DockerAgentInstaller) poll(ctx context.Context) error {
	interval := i.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		i.mu.Lock()
		requestID, expectedVersion := i.state.RequestID, i.state.TargetVersion
		i.mu.Unlock()
		var status DockerRequestStatus
		if err := i.API.Do(ctx, http.MethodGet, fmt.Sprintf("/v1/update-requests/%s", requestID), "", nil, &status, http.StatusOK); err != nil {
			return fmt.Errorf("%w: query Docker update request: %v", ErrUpdateRecoveryPending, err)
		}
		if status.RequestID != requestID || status.SchemaVersion != 1 {
			return fmt.Errorf("%w: Docker agent returned mismatched request", ErrUpdateRecoveryPending)
		}
		i.mu.Lock()
		previousStatus := i.state.Status
		previousCurrentVersion := i.state.ReportedCurrentVersion
		previousTargetVersion := i.state.ReportedTargetVersion
		i.state.Status = status.Status
		i.state.ReportedCurrentVersion = status.CurrentVersion
		i.state.ReportedTargetVersion = status.TargetVersion
		i.mu.Unlock()
		if err := i.save(); err != nil {
			i.mu.Lock()
			i.state.Status = previousStatus
			i.state.ReportedCurrentVersion = previousCurrentVersion
			i.state.ReportedTargetVersion = previousTargetVersion
			i.mu.Unlock()
			return fmt.Errorf("%w: persist Docker update status: %v", ErrUpdateRecoveryPending, err)
		}
		switch status.Status {
		case "succeeded":
			if status.TargetVersion != expectedVersion || status.CurrentVersion != expectedVersion {
				return fmt.Errorf("%w: Docker agent reported a different replacement version", ErrUpdateRecoveryPending)
			}
			return nil
		case "failed":
			return fmt.Errorf("Docker update failed: %s", status.FailureCode)
		case "rolled_back":
			return fmt.Errorf("%w: %s", ErrUpdateRolledBack, status.FailureCode)
		case "cancelled":
			return ErrUpdateCancelled
		case "accepted", "pulling", "snapshotting", "replacing", "validating", "rolling_back":
		default:
			return fmt.Errorf("%w: invalid Docker update status", ErrUpdateRecoveryPending)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (i *DockerAgentInstaller) Resume(ctx context.Context) error {
	i.mu.Lock()
	requestID := i.state.RequestID
	creating := i.state.Status == "creating" && i.state.CreateIdempotencyKey != ""
	i.mu.Unlock()
	if requestID == "" && creating {
		if err := i.createRequest(ctx); err != nil {
			return err
		}
	} else if requestID == "" {
		return errors.New("Docker update request recovery state is unavailable")
	}
	return i.poll(ctx)
}

func (i *DockerAgentInstaller) Cancel(ctx context.Context) error {
	i.mu.Lock()
	requestID := i.state.RequestID
	if requestID == "" {
		i.mu.Unlock()
		return nil
	}
	if i.state.CancelIdempotencyKey == "" {
		i.state.CancelIdempotencyKey = randomGeneration()
	}
	key := i.state.CancelIdempotencyKey
	i.mu.Unlock()
	if err := i.save(); err != nil {
		return err
	}
	if err := i.API.Do(ctx, http.MethodPost, fmt.Sprintf("/v1/update-requests/%s/cancel", requestID), key, struct {
		SchemaVersion int `json:"schema_version"`
	}{1}, nil, http.StatusNoContent, http.StatusOK); err != nil {
		return err
	}
	i.mu.Lock()
	i.state.Status = "cancelled"
	i.mu.Unlock()
	return i.save()
}
func (i *DockerAgentInstaller) Validate(_ context.Context, release ReleaseMetadata) error {
	i.mu.Lock()
	state := i.state
	i.mu.Unlock()
	if state.Status != "succeeded" {
		return errors.New("Docker agent did not validate replacement")
	}
	if state.TargetVersion != release.Version || state.ReportedTargetVersion != release.Version || state.ReportedCurrentVersion != release.Version {
		return errors.New("Docker agent validated a different version")
	}
	return nil
}
func (i *DockerAgentInstaller) Rollback(context.Context, any) error {
	i.mu.Lock()
	status := i.state.Status
	i.mu.Unlock()
	if status == "rolled_back" {
		return nil
	}
	return errors.New("Docker agent owns rollback")
}
func (i *DockerAgentInstaller) save() error {
	if i.StatePath == "" {
		return nil
	}
	i.mu.Lock()
	state := i.state
	writer := i.stateWriter
	i.mu.Unlock()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if writer == nil {
		writer = atomicWriteState
	}
	return writer(i.StatePath, data)
}
func (i *DockerAgentInstaller) Load() error {
	data, err := os.ReadFile(i.StatePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state dockerPersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	i.mu.Lock()
	i.state = state
	i.mu.Unlock()
	return nil
}
