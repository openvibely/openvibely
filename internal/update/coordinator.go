package update

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"
)

const (
	StateIdle           = "idle"
	StateChecking       = "checking"
	StateAvailable      = "available"
	StateStaging        = "staging"
	StateWaitingForIdle = "waiting_for_idle"
	StateReady          = "ready"
	StateApplying       = "applying"
	StateRestarting     = "restarting"
	StateValidating     = "validating"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
	StateRollingBack    = "rolling_back"
	StateRolledBack     = "rolled_back"
)

type Installer interface {
	Stage(context.Context, VerifiedRelease) (any, error)
	Apply(context.Context, any) error
	Validate(context.Context, ReleaseMetadata) error
	Rollback(context.Context, any) error
}
type restartValidatingInstaller interface {
	RequiresRestartValidation() bool
}

type resumableInstaller interface {
	Resume(context.Context) error
}

type binaryRestartRecoveryInstaller interface {
	RecoverBinaryRestart(context.Context, LocalStagedUpdate) error
}

type recoveryReadyInstaller interface {
	RecoveryReady() bool
}

type ManagedUpdateState struct {
	Active          bool
	State           string
	DesiredVersion  string
	ReleaseNotesURL string
	Error           string
}

var (
	ErrUpdateCancelled       = errors.New("update cancelled")
	ErrUpdateRolledBack      = errors.New("update rolled back")
	ErrUpdateRecoveryPending = errors.New("update recovery pending")
	ErrUpdateRetryable       = errors.New("update operation can be retried")
)

type CoordinatorSnapshot struct {
	State              string           `json:"state"`
	CurrentVersion     string           `json:"current_version"`
	Distribution       string           `json:"distribution"`
	Channel            string           `json:"channel"`
	Release            *VerifiedRelease `json:"release,omitempty"`
	Drain              DrainStatus      `json:"drain"`
	ConfigurationError string           `json:"configuration_error,omitempty"`
	Error              string           `json:"error,omitempty"`
	Manual             bool             `json:"manual"`
	Staged             bool             `json:"staged"`
}
type Coordinator struct {
	mu                         sync.RWMutex
	client                     *Client
	current                    CurrentBuild
	channel                    string
	drain                      *DrainManager
	installer                  Installer
	protectedDataPaths         []string
	desktopHealthURL           string
	desktopArguments           []string
	desktopWorkingDirectory    string
	desktopShutdown            func()
	manual                     bool
	state                      string
	release                    *VerifiedRelease
	staged                     any
	configError, lastError     string
	wakeDispatch               func()
	persistence                string
	stateWriter                func(string, []byte) error
	operationGeneration        string
	cleanupGeneration          string
	accepted                   bool
	acceptanceLease            time.Duration
	acceptanceSupervisor       bool
	managedStateProvider       func() ManagedUpdateState
	recoveryCtx                context.Context
	recoveryRetryInterval      time.Duration
	binaryOutcomeReadHook      func()
	binaryRecoveryLeaseHook    func()
	recoveryOnce               sync.Once
	checksOnce                 sync.Once
	updateNotificationsEnabled bool
}

func NewCoordinator(client *Client, current CurrentBuild, channel string, drain *DrainManager, installer Installer, manual bool, configError string, wakeDispatch func()) *Coordinator {
	return &Coordinator{client: client, current: current, channel: channel, drain: drain, installer: installer, manual: manual, state: StateIdle, configError: configError, wakeDispatch: wakeDispatch, updateNotificationsEnabled: true}
}

func (c *Coordinator) SetUpdateNotificationsEnabled(enabled bool) {
	c.mu.Lock()
	c.updateNotificationsEnabled = enabled
	c.mu.Unlock()
}

func (c *Coordinator) SetProtectedDataPaths(paths []string) {
	c.mu.Lock()
	c.protectedDataPaths = append([]string(nil), paths...)
	c.mu.Unlock()
}

func (c *Coordinator) SetDesktopRelaunchContext(healthURL string, arguments []string, workingDirectory string, shutdown func()) {
	c.mu.Lock()
	c.desktopHealthURL = healthURL
	c.desktopArguments = append([]string(nil), arguments...)
	c.desktopWorkingDirectory = workingDirectory
	c.desktopShutdown = shutdown
	c.mu.Unlock()
}

func (c *Coordinator) SetPersistence(path string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persistence = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var persisted struct {
		State               string             `json:"state"`
		Release             *VerifiedRelease   `json:"release"`
		StagedLocal         *LocalStagedUpdate `json:"staged_local,omitempty"`
		StagedRelease       *VerifiedRelease   `json:"staged_release,omitempty"`
		Error               string             `json:"error,omitempty"`
		OperationGeneration string             `json:"operation_generation,omitempty"`
		Accepted            bool               `json:"accepted,omitempty"`
		AcceptanceLease     time.Duration      `json:"acceptance_lease,omitempty"`
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		return err
	}
	c.state, c.release, c.lastError, c.operationGeneration = persisted.State, persisted.Release, persisted.Error, persisted.OperationGeneration
	c.accepted, c.acceptanceLease = persisted.Accepted, persisted.AcceptanceLease
	if persisted.StagedLocal != nil {
		c.staged = *persisted.StagedLocal
	}
	if persisted.StagedRelease != nil {
		c.staged = *persisted.StagedRelease
	}
	if c.accepted {
		if c.release == nil || c.acceptanceLease <= 0 {
			return errors.New("persisted update acceptance is incomplete")
		}
		if c.drain != nil {
			status := c.drain.Status()
			switch {
			case status.State != DrainStateIdle && c.operationGeneration == "":
				if c.staged == nil && !c.manual {
					return errors.New("persisted accepted update drain has no staged artifact")
				}
				c.state, c.operationGeneration = StateWaitingForIdle, status.Generation
				if err := c.persistLocked(); err != nil {
					return err
				}
			case status.State == DrainStateIdle && c.state == StateStaging:
				// Acceptance was durable before staging began. Retry staging after
				// restart instead of discarding the user's approval.
				c.state, c.lastError = StateAvailable, ""
				if err := c.persistLocked(); err != nil {
					return err
				}
			}
		}
	}
	packagedRestart := c.state == StateRestarting && (c.current.Distribution == "binary" || c.current.Distribution == "desktop")
	if c.release != nil && c.current.Version == c.release.Metadata.Version && isTransitionState(c.state) && !packagedRestart {
		if c.drain != nil {
			status := c.drain.Status()
			generation := c.operationGeneration
			if generation == "" {
				generation = status.Generation
			}
			if status.State != DrainStateIdle && (generation != status.Generation || !c.drain.Release(generation)) {
				return errors.New("persisted successful update does not own the active drain generation")
			}
		}
		c.state, c.lastError, c.operationGeneration = StateSucceeded, "", ""
		c.clearAcceptanceLocked()
		if err := c.persistLocked(); err != nil {
			return err
		}
	} else if c.drain != nil && isTransitionState(c.state) {
		status := c.drain.Status()
		switch {
		case status.State == DrainStateIdle:
			if c.accepted {
				// A crash can follow durable drain creation and an ambiguous
				// waiting-for-idle coordinator write, then durable drain release.
				// Preserve the user's approval so recovery retries the handoff.
				c.state, c.lastError, c.operationGeneration = StateAvailable, "", ""
			} else {
				c.state, c.lastError, c.operationGeneration = StateIdle, "", ""
				c.clearAcceptanceLocked()
			}
		case c.operationGeneration == "":
			c.operationGeneration = status.Generation
		case c.operationGeneration != status.Generation:
			return errors.New("persisted update operation does not own the active drain generation")
		}
		if err := c.persistLocked(); err != nil {
			return err
		}
	}
	return nil
}
func (c *Coordinator) persistLocked() error {
	if c.persistence == "" {
		return nil
	}
	payload := struct {
		State               string             `json:"state"`
		Release             *VerifiedRelease   `json:"release,omitempty"`
		StagedLocal         *LocalStagedUpdate `json:"staged_local,omitempty"`
		StagedRelease       *VerifiedRelease   `json:"staged_release,omitempty"`
		Error               string             `json:"error,omitempty"`
		OperationGeneration string             `json:"operation_generation,omitempty"`
		Accepted            bool               `json:"accepted,omitempty"`
		AcceptanceLease     time.Duration      `json:"acceptance_lease,omitempty"`
	}{State: c.state, Release: c.release, Error: c.lastError, OperationGeneration: c.operationGeneration, Accepted: c.accepted, AcceptanceLease: c.acceptanceLease}
	switch staged := c.staged.(type) {
	case LocalStagedUpdate:
		payload.StagedLocal = &staged
	case VerifiedRelease:
		payload.StagedRelease = &staged
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	writer := c.stateWriter
	if writer == nil {
		writer = atomicWriteState
	}
	return writer(c.persistence, data)
}

func (c *Coordinator) clearAcceptanceLocked() {
	c.accepted = false
	c.acceptanceLease = 0
}

func (c *Coordinator) BindWailsUpdater(updater *wailsupdater.Updater) error {
	if updater == nil {
		return errors.New("Wails updater is nil")
	}
	c.mu.Lock()
	if c.current.Distribution != "desktop" {
		c.mu.Unlock()
		return errors.New("Wails updater is only valid for desktop builds")
	}
	provider := &WailsProvider{Client: c.client, Current: c.current, Release: c.release}
	installer := &WailsInstaller{
		Updater: updater, Provider: provider,
		ProtectedDataPaths: append([]string(nil), c.protectedDataPaths...),
		HealthURL:          c.desktopHealthURL, Arguments: append([]string(nil), c.desktopArguments...),
		WorkingDirectory: c.desktopWorkingDirectory, Shutdown: c.desktopShutdown,
	}
	c.installer = installer
	needsStage := c.state == StateAvailable && c.release != nil && c.staged == nil
	stageCtx := c.recoveryCtx
	if stageCtx == nil {
		stageCtx = context.Background()
	}
	c.mu.Unlock()
	c.StartRecovery(nil)
	if needsStage {
		go func() { _ = c.Stage(stageCtx) }()
	}
	return nil
}
func isTransitionState(state string) bool {
	switch state {
	case StateStaging, StateWaitingForIdle, StateReady, StateApplying, StateRestarting, StateValidating, StateRollingBack:
		return true
	default:
		return false
	}
}

func (c *Coordinator) SetManagedStateProvider(provider func() ManagedUpdateState) {
	c.mu.Lock()
	c.managedStateProvider = provider
	c.mu.Unlock()
}

func (c *Coordinator) Visible() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current.Distribution == "source" {
		return false
	}
	return c.updateNotificationsEnabled || c.accepted || c.operationGeneration != "" || isTransitionState(c.state)
}
func (c *Coordinator) Snapshot() CoordinatorSnapshot {
	c.mu.RLock()
	snapshot := CoordinatorSnapshot{State: c.state, CurrentVersion: c.current.Version, Distribution: c.current.Distribution, Channel: c.channel, Release: c.release, ConfigurationError: c.configError, Error: c.lastError, Manual: c.manual, Staged: c.staged != nil}
	provider := c.managedStateProvider
	c.mu.RUnlock()
	if c.drain != nil {
		snapshot.Drain = c.drain.Status()
	}
	if provider != nil {
		managed := provider()
		if managed.Active {
			snapshot.State = managed.State
			snapshot.Error = managed.Error
			if managed.DesiredVersion != "" {
				release := VerifiedRelease{Metadata: ReleaseMetadata{Version: managed.DesiredVersion, ReleaseNotesURL: managed.ReleaseNotesURL, Channel: c.channel}}
				snapshot.Release = &release
			}
		}
	}
	return snapshot
}
func (c *Coordinator) Check(ctx context.Context) error {
	if !c.Visible() {
		_, _, err := c.client.CheckIfDue(ctx, c.current)
		return err
	}
	c.mu.Lock()
	if isTransitionState(c.state) || c.accepted {
		c.mu.Unlock()
		return nil
	}
	previousState, previousError := c.state, c.lastError
	c.state, c.lastError = StateChecking, ""
	if err := c.persistLocked(); err != nil {
		c.state, c.lastError = previousState, previousError
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	release, checked, err := c.client.CheckIfDue(ctx, c.current)
	if err != nil {
		c.mu.Lock()
		if c.state == StateChecking {
			c.state, c.lastError = StateFailed, err.Error()
			if persistErr := c.persistLocked(); persistErr != nil {
				c.mu.Unlock()
				return errors.Join(err, persistErr)
			}
		}
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	if c.state != StateChecking {
		c.mu.Unlock()
		return nil
	}
	if checked {
		c.release = release
		c.staged = nil
	} else if release != nil && c.release == nil {
		c.release = release
	}
	if c.release != nil {
		c.state = StateAvailable
	} else {
		c.state = StateIdle
	}
	if err := c.persistLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	shouldStage := c.release != nil && c.installer != nil && !c.manual && (c.current.Distribution == "binary" || c.current.Distribution == "desktop")
	c.mu.Unlock()
	if shouldStage {
		return c.Stage(ctx)
	}
	return nil
}
func (c *Coordinator) Stage(ctx context.Context) error {
	c.mu.Lock()
	if c.state != StateAvailable && c.state != StateFailed {
		state := c.state
		c.mu.Unlock()
		return errors.New("cannot stage update while coordinator is " + state)
	}
	if c.release == nil {
		c.mu.Unlock()
		return errors.New("no verified release available")
	}
	release := *c.release
	installer := c.installer
	if installer == nil && !c.manual {
		c.mu.Unlock()
		return errors.New("installer unavailable")
	}
	if err := c.client.ValidateForInstall(release, c.current); err != nil {
		c.state, c.lastError = StateFailed, err.Error()
		c.mu.Unlock()
		return err
	}
	if c.configError != "" {
		err := errors.New(c.configError)
		c.mu.Unlock()
		return err
	}
	previousState := c.state
	c.state = StateStaging
	if err := c.persistLocked(); err != nil {
		c.state = previousState
		c.mu.Unlock()
		return errors.Join(ErrUpdateRetryable, err)
	}
	c.mu.Unlock()
	if installer == nil {
		if c.manual {
			c.setState(StateAvailable, "")
			return nil
		}
		return errors.New("installer unavailable")
	}
	staged, err := installer.Stage(ctx, release)
	if err != nil {
		c.setState(StateFailed, err.Error())
		return err
	}
	c.mu.Lock()
	c.staged = staged
	c.state, c.lastError = StateAvailable, ""
	if err := c.persistLocked(); err != nil {
		c.state, c.lastError = StateFailed, "persist staged update: "+err.Error()
		c.mu.Unlock()
		return errors.Join(ErrUpdateRetryable, err)
	}
	c.mu.Unlock()
	return nil
}
func (c *Coordinator) Accept(_ context.Context, lease time.Duration) error {
	c.mu.Lock()
	if !c.accepted {
		if lease <= 0 {
			c.mu.Unlock()
			return errors.New("update acceptance lease must be positive")
		}
		if c.drain == nil {
			c.mu.Unlock()
			return errors.New("drain manager unavailable")
		}
		if c.drain.Status().State != DrainStateIdle {
			c.mu.Unlock()
			return errors.New("cannot accept update while another drain is active")
		}
		if c.state != StateAvailable && c.state != StateFailed {
			state := c.state
			c.mu.Unlock()
			return errors.New("cannot accept update while coordinator is " + state)
		}
		if c.release == nil {
			c.mu.Unlock()
			return errors.New("no verified release available")
		}
		if c.installer == nil && !c.manual {
			c.mu.Unlock()
			return errors.New("installer unavailable")
		}
		if !c.manual && c.current.Distribution != "docker" && c.staged == nil {
			c.mu.Unlock()
			return errors.New("update replacement is not staged")
		}
		if c.configError != "" {
			err := errors.New(c.configError)
			c.mu.Unlock()
			return err
		}
		if err := c.client.ValidateForInstall(*c.release, c.current); err != nil {
			c.mu.Unlock()
			return err
		}
		c.accepted, c.acceptanceLease = true, lease
		if err := c.persistLocked(); err != nil {
			c.clearAcceptanceLocked()
			c.mu.Unlock()
			return err
		}
	}
	c.mu.Unlock()
	c.startAcceptedUpdateSupervisor()
	return nil
}

func (c *Coordinator) startAcceptedUpdateSupervisor() {
	c.mu.Lock()
	if !c.accepted || c.acceptanceSupervisor {
		c.mu.Unlock()
		return
	}
	ctx := c.recoveryCtx
	if ctx == nil {
		ctx = context.Background()
	}
	c.acceptanceSupervisor = true
	c.mu.Unlock()
	go c.superviseAcceptedUpdate(ctx)
}

func isRetryableAcceptedUpdateError(err error) bool {
	if errors.Is(err, ErrUpdateRetryable) || errors.Is(err, ErrUpdateRecoveryPending) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}

func (c *Coordinator) settleAcceptedUpdateFailure(ctx context.Context, cause error) {
	for {
		c.mu.Lock()
		if !c.accepted || c.operationGeneration != "" {
			c.mu.Unlock()
			return
		}
		lease := c.acceptanceLease
		c.state, c.lastError = StateFailed, cause.Error()
		c.clearAcceptanceLocked()
		err := c.persistLocked()
		if err != nil {
			// The durable record still owns the acceptance. Keep supervising it
			// rather than exposing an in-memory settlement that restart would undo.
			c.accepted, c.acceptanceLease = true, lease
			c.lastError = cause.Error() + "; settle accepted update: " + err.Error()
		}
		c.mu.Unlock()
		if err == nil {
			return
		}
		timer := time.NewTimer(c.recoveryRetryDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Coordinator) superviseAcceptedUpdate(ctx context.Context) {
	defer func() {
		c.mu.Lock()
		c.acceptanceSupervisor = false
		c.mu.Unlock()
	}()
	for {
		c.mu.RLock()
		accepted := c.accepted && c.operationGeneration == ""
		lease := c.acceptanceLease
		c.mu.RUnlock()
		if !accepted {
			return
		}
		err := c.continueAcceptedUpdate(ctx, lease)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !isRetryableAcceptedUpdateError(err) {
			c.settleAcceptedUpdateFailure(ctx, err)
			return
		}
		c.mu.RLock()
		accepted = c.accepted && c.operationGeneration == ""
		c.mu.RUnlock()
		if !accepted {
			return
		}
		timer := time.NewTimer(c.recoveryRetryDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Coordinator) continueAcceptedUpdate(ctx context.Context, lease time.Duration) error {
	c.mu.RLock()
	readyToApply := c.state == StateAvailable && (c.manual || c.staged != nil)
	c.mu.RUnlock()
	if !readyToApply {
		return errors.New("accepted update replacement is not staged")
	}
	return c.Apply(ctx, lease)
}

func (c *Coordinator) Apply(ctx context.Context, lease time.Duration) error {
	if c.drain == nil {
		return errors.New("drain manager unavailable")
	}
	c.mu.Lock()
	if c.state != StateAvailable {
		state := c.state
		c.mu.Unlock()
		return errors.New("cannot apply update while coordinator is " + state)
	}
	if c.release == nil {
		c.mu.Unlock()
		return errors.New("no verified release available")
	}
	if c.configError != "" {
		err := errors.New(c.configError)
		c.mu.Unlock()
		return err
	}
	release := *c.release
	staged := c.staged
	installer := c.installer
	if !c.manual && staged == nil {
		c.mu.Unlock()
		return errors.New("update must be staged before apply")
	}
	if err := c.client.ValidateForInstall(release, c.current); err != nil {
		c.state, c.lastError = StateFailed, err.Error()
		c.mu.Unlock()
		return err
	}
	status, err := c.drain.BeginDrain(DrainRequest{Lease: lease})
	if err != nil {
		c.mu.Unlock()
		return err
	}
	accepted := c.accepted
	c.state, c.lastError, c.operationGeneration = StateWaitingForIdle, "", status.Generation
	if err := c.persistLocked(); err != nil {
		c.mu.Unlock()
		if accepted {
			return c.resetAcceptedDrainHandoff(ctx, status.Generation, err)
		}
		cleanupErr := c.completeGeneration(status.Generation, StateAvailable, "")
		if cleanupErr != nil {
			c.startCompletionSupervisor(status.Generation, StateAvailable, "")
			return errors.Join(err, cleanupErr)
		}
		return err
	}
	c.mu.Unlock()
	go c.waitAndApply(context.Background(), release, staged, installer, status.Generation, false)
	return nil
}

func (c *Coordinator) resetAcceptedDrainHandoff(ctx context.Context, generation string, cause error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if !c.accepted || (c.operationGeneration != "" && c.operationGeneration != generation) {
			c.mu.Unlock()
			return errors.Join(ErrUpdateRetryable, cause)
		}
		status := c.drain.Status()
		if status.State != DrainStateIdle {
			if status.Generation != generation || !c.drain.Release(generation) {
				c.lastError = "release accepted update drain after handoff persistence failure"
				c.mu.Unlock()
				if !waitForUpdateRetry(ctx, c.recoveryRetryDelay()) {
					return ctx.Err()
				}
				continue
			}
		}
		c.state, c.lastError, c.operationGeneration = StateAvailable, cause.Error(), ""
		err := c.persistLocked()
		c.mu.Unlock()
		if err == nil {
			return errors.Join(ErrUpdateRetryable, cause)
		}
		if !waitForUpdateRetry(ctx, c.recoveryRetryDelay()) {
			return ctx.Err()
		}
	}
}

func waitForUpdateRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Coordinator) waitAndApply(ctx context.Context, release VerifiedRelease, staged any, installer Installer, generation string, preserveOnCancel bool) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if !preserveOnCancel {
				c.abortGeneration(generation, ctx.Err())
			}
			return
		case <-ticker.C:
			status := c.drain.Status()
			if status.State == DrainStateIdle {
				c.completeGenerationWithRetry(ctx, generation, StateIdle, "")
				return
			}
			if status.Generation != generation {
				return
			}
			if status.State != DrainStateReady {
				continue
			}
			if c.manual {
				c.mu.RLock()
				alreadyReady := c.operationGeneration == generation && c.state == StateReady
				c.mu.RUnlock()
				if !alreadyReady && !c.setOperationState(generation, StateReady, "") {
					// Retain the waiter so the unowned drain remains autonomously
					// supervised through lease expiry while persistence recovers.
					continue
				}
				// Manual Docker preparation remains supervised until the operator
				// restarts the container, cancels, or the unowned lease expires.
				continue
			}
			if installer == nil {
				c.abortGeneration(generation, errors.New("installer unavailable"))
				return
			}
			// Signed metadata may expire while active work drains. Revalidate at
			// the last possible boundary before ownership and installation.
			if err := c.client.ValidateForInstall(release, c.current); err != nil {
				c.abortGeneration(generation, err)
				return
			}
			if !c.drain.TakeOwnership(status.Generation) && !(preserveOnCancel && c.drain.Owns(generation)) {
				continue
			}
			restartValidation := false
			if restartInstaller, ok := installer.(restartValidatingInstaller); ok {
				restartValidation = restartInstaller.RequiresRestartValidation()
			}
			if restartValidation {
				if err := c.persistOperationState(generation, StateRestarting, ""); err != nil {
					c.superviseOwnedTransitionFailure(generation, err)
					return
				}
			} else {
				if err := c.persistOperationState(generation, StateApplying, ""); err != nil {
					c.superviseOwnedTransitionFailure(generation, err)
					return
				}
			}
			if err := installer.Apply(ctx, staged); err != nil {
				switch {
				case errors.Is(err, ErrUpdateRecoveryPending):
					if resumable, ok := installer.(resumableInstaller); ok {
						c.resumeInstallerUntilSettled(ctx, resumable, installer, staged, generation, release.Metadata)
					}
				case errors.Is(err, ErrUpdateCancelled):
					c.completeGenerationWithRetry(ctx, generation, StateIdle, "")
				case errors.Is(err, ErrUpdateRolledBack):
					c.completeGenerationWithRetry(ctx, generation, StateRolledBack, err.Error())
				case restartValidation:
					c.abortGeneration(generation, err)
				default:
					c.rollback(ctx, installer, staged, generation, err)
				}
				return
			}
			if restartValidation {
				return
			}
			if !c.persistOperationStateWithRetry(ctx, generation, StateValidating, "") {
				return
			}
			if err := installer.Validate(ctx, release.Metadata); err != nil {
				c.rollback(ctx, installer, staged, generation, err)
				return
			}
			c.completeGenerationWithRetry(ctx, generation, StateSucceeded, "")
			return
		}
	}
}
func (c *Coordinator) rollback(ctx context.Context, installer Installer, staged any, generation string, cause error) {
	if !c.persistOperationStateWithRetry(ctx, generation, StateRollingBack, cause.Error()) {
		return
	}
	if err := installer.Rollback(ctx, staged); err != nil {
		message := cause.Error() + "; rollback: " + err.Error()
		_ = c.failGeneration(generation, message)
		return
	}
	c.completeGenerationWithRetry(ctx, generation, StateRolledBack, cause.Error())
}

func (c *Coordinator) persistOperationStateWithRetry(ctx context.Context, generation, state, message string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := c.persistOperationState(generation, state, message); err == nil {
			return true
		}
		c.mu.RLock()
		active := generation != "" && c.operationGeneration == generation
		c.mu.RUnlock()
		if !active {
			return false
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (c *Coordinator) setOperationState(generation, state, message string) bool {
	return c.persistOperationState(generation, state, message) == nil
}

func (c *Coordinator) persistOperationState(generation, state, message string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation == "" || c.operationGeneration != generation {
		return errors.New("update operation generation changed")
	}
	c.state, c.lastError = state, message
	if err := c.persistLocked(); err != nil {
		c.state, c.lastError = StateFailed, "persist update transition: "+err.Error()
		return err
	}
	return nil
}

// superviseOwnedTransitionFailure handles the only safe abort boundary after
// drain ownership: the replacement has not started because its transition was
// not durable. It retries exact-generation release and terminal persistence so
// a transient storage failure cannot strand admission or permit an unrecorded
// installer side effect. A concurrent durable cancellation may settle the same
// generation; in that case this supervisor observes the generation change and
// exits.
func (c *Coordinator) superviseOwnedTransitionFailure(generation string, cause error) {
	message := "persist update transition: " + cause.Error()
	c.mu.Lock()
	if generation == "" || c.operationGeneration != generation {
		c.mu.Unlock()
		return
	}
	c.cleanupGeneration = generation
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.cleanupGeneration == generation {
			c.cleanupGeneration = ""
		}
		c.mu.Unlock()
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		if generation == "" || c.operationGeneration != generation {
			c.mu.Unlock()
			return
		}
		status := c.drain.Status()
		if status.State != DrainStateIdle {
			if status.Generation != generation || !c.drain.Release(generation) {
				// Keep the operation in a transition state while cleanup is pending
				// so periodic checks and staging cannot replace it.
				c.state, c.lastError = StateWaitingForIdle, message
				c.mu.Unlock()
				<-ticker.C
				continue
			}
		}
		c.operationGeneration = ""
		c.state, c.lastError = StateFailed, message
		c.clearAcceptanceLocked()
		if err := c.persistLocked(); err != nil {
			// The drain is already durably idle. Retain the generation in memory
			// and retry the terminal record; on a crash, SetPersistence observes
			// the durable idle drain and normalizes the older transition record.
			c.operationGeneration = generation
			c.state, c.lastError = StateWaitingForIdle, message+"; persist cleanup: "+err.Error()
			c.mu.Unlock()
			<-ticker.C
			continue
		}
		c.mu.Unlock()
		if c.wakeDispatch != nil {
			c.wakeDispatch()
		}
		return
	}
}

func (c *Coordinator) failGeneration(generation, message string) error {
	if generation == "" {
		c.mu.RLock()
		generation = c.operationGeneration
		c.mu.RUnlock()
	}
	if generation == "" && c.drain != nil {
		status := c.drain.Status()
		if status.State != DrainStateIdle && c.drain.Owns(status.Generation) {
			c.mu.Lock()
			if c.operationGeneration == "" {
				c.operationGeneration = status.Generation
			}
			generation = c.operationGeneration
			c.mu.Unlock()
		}
	}
	if generation != "" {
		err := c.completeGeneration(generation, StateFailed, message)
		if err != nil {
			c.startCompletionSupervisor(generation, StateFailed, message)
		}
		return err
	}
	c.mu.Lock()
	c.state, c.lastError = StateFailed, message
	c.clearAcceptanceLocked()
	err := c.persistLocked()
	c.mu.Unlock()
	return err
}

func (c *Coordinator) completeGeneration(generation, state, message string) error {
	c.mu.Lock()
	if generation == "" || c.operationGeneration != generation {
		c.mu.Unlock()
		return nil
	}
	if c.drain != nil {
		status := c.drain.Status()
		if status.State != DrainStateIdle && !c.drain.Release(generation) {
			err := errors.New("failed to durably release update drain")
			c.lastError = err.Error()
			c.mu.Unlock()
			return err
		}
	}
	c.operationGeneration = ""
	c.state, c.lastError = state, message
	c.clearAcceptanceLocked()
	if err := c.persistLocked(); err != nil {
		// The drain may already be durably idle. Retain this exact generation in
		// memory so terminal persistence remains supervised and retryable.
		c.operationGeneration = generation
		c.state, c.lastError = StateWaitingForIdle, "persist terminal update state: "+err.Error()
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	if c.wakeDispatch != nil {
		c.wakeDispatch()
	}
	return nil
}

func (c *Coordinator) completeGenerationWithRetry(ctx context.Context, generation, state, message string) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := c.completeGeneration(generation, state, message); err == nil {
			return
		}
		c.mu.RLock()
		active := generation != "" && c.operationGeneration == generation
		c.mu.RUnlock()
		if !active {
			return
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Coordinator) startCompletionSupervisor(generation, state, message string) {
	c.mu.RLock()
	ctx := c.recoveryCtx
	c.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go c.completeGenerationWithRetry(ctx, generation, state, message)
}

func (c *Coordinator) abortGeneration(generation string, cause error) {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	c.completeGenerationWithRetry(context.Background(), generation, StateFailed, message)
}

func (c *Coordinator) Cancel() error {
	c.mu.RLock()
	installer, state, generation, managedProvider := c.installer, c.state, c.operationGeneration, c.managedStateProvider
	c.mu.RUnlock()
	if managedProvider != nil && managedProvider().Active {
		return errors.New("Hosted updates can only be cancelled by the Hosted administrator")
	}
	if generation == "" {
		if state == StateIdle {
			return nil
		}
		return errors.New("no update drain is active")
	}
	if state == StateRestarting || state == StateValidating || state == StateRollingBack || state == StateFailed {
		return errors.New("update replacement can no longer be cancelled")
	}
	if state == StateApplying {
		canceler, ok := installer.(interface{ Cancel(context.Context) error })
		if !ok {
			return errors.New("update replacement can no longer be cancelled")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := canceler.Cancel(ctx)
		cancel()
		if err != nil {
			return err
		}
	}
	err := c.completeGeneration(generation, StateIdle, "")
	if err != nil {
		c.mu.RLock()
		cleanupActive := c.cleanupGeneration == generation
		c.mu.RUnlock()
		if !cleanupActive {
			c.startCompletionSupervisor(generation, StateIdle, "")
		}
	}
	return err
}
func (c *Coordinator) setState(state, message string) {
	c.mu.Lock()
	c.state, c.lastError = state, message
	if err := c.persistLocked(); err != nil {
		c.state, c.lastError = StateFailed, "persist update state: "+err.Error()
	}
	c.mu.Unlock()
}
func (c *Coordinator) recoveryRetryDelay() time.Duration {
	c.mu.RLock()
	delay := c.recoveryRetryInterval
	c.mu.RUnlock()
	if delay <= 0 {
		return time.Second
	}
	return delay
}

func (c *Coordinator) operationIsActive(generation string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.operationGeneration == generation && (c.state == StateApplying || c.state == StateValidating)
}

func (c *Coordinator) resumeInstallerUntilSettled(ctx context.Context, resumable resumableInstaller, installer Installer, staged any, generation string, metadata ReleaseMetadata) {
	for c.operationIsActive(generation) {
		err := resumable.Resume(ctx)
		if !c.operationIsActive(generation) {
			return
		}
		if errors.Is(err, ErrUpdateRecoveryPending) {
			timer := time.NewTimer(c.recoveryRetryDelay())
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return
			case errors.Is(err, ErrUpdateCancelled):
				c.completeGenerationWithRetry(ctx, generation, StateIdle, "")
			case errors.Is(err, ErrUpdateRolledBack):
				c.completeGenerationWithRetry(ctx, generation, StateRolledBack, err.Error())
			default:
				c.rollback(ctx, installer, staged, generation, err)
			}
			return
		}
		if !c.operationIsActive(generation) {
			return
		}
		if err := installer.Validate(ctx, metadata); err != nil {
			c.rollback(ctx, installer, staged, generation, err)
			return
		}
		c.completeGenerationWithRetry(ctx, generation, StateSucceeded, "")
		return
	}
}

func (c *Coordinator) recoverDeadBinaryHelper(ctx context.Context, staged LocalStagedUpdate, generation string, outcome binaryHelperOutcome, currentVersion string) bool {
	switch outcome.State {
	case binaryOutcomePrepared, binaryOutcomePending, binaryOutcomeAuthorized, binaryOutcomeParentExited,
		binaryOutcomeBackupPublished, binaryOutcomeTargetPublished, binaryOutcomeValidating, binaryOutcomeRollingBack:
	default:
		return false
	}
	lease, acquired, err := tryAcquireBinaryHelperLease(binaryHelperLeasePath(staged))
	if err != nil || !acquired {
		return false
	}
	var leaseHeld = true
	defer func() {
		if leaseHeld {
			_ = lease.Close()
		}
	}()
	if c.binaryRecoveryLeaseHook != nil {
		c.binaryRecoveryLeaseHook()
	}

	startRecovery := func() bool {
		if err := writeBinaryHelperRecoveryClaimWithRetry(ctx, staged); err != nil {
			return false
		}
		if err := lease.Close(); err != nil {
			return false
		}
		leaseHeld = false
		recovery, ok := c.installer.(binaryRestartRecoveryInstaller)
		if !ok {
			return false
		}
		return recovery.RecoverBinaryRestart(ctx, staged) == nil
	}

	if outcome.State == binaryOutcomeRollingBack {
		if c.current.Distribution == "desktop" {
			return startRecovery()
		}
		if currentVersion == staged.PreviousVersion {
			if _, err := os.Stat(staged.BackupPath); os.IsNotExist(err) {
				if err := ensureBootableBinary(staged.InstallPath, staged.BackupPath); err != nil {
					return false
				}
				if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeRolledBack); err != nil {
					return false
				}
				c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update helper died after restoring the predecessor")
				return true
			}
		}
		return startRecovery()
	}
	if currentVersion == staged.Version {
		if c.current.Distribution == "desktop" {
			return startRecovery()
		}
		if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeSucceeded); err != nil {
			return false
		}
		c.completeGenerationWithRetry(ctx, generation, StateSucceeded, "")
		return true
	}
	if currentVersion != staged.PreviousVersion {
		return false
	}
	if _, err := os.Stat(staged.ArtifactPath); err == nil {
		if c.current.Distribution != "desktop" {
			if err := ensureBootableBinary(staged.InstallPath, staged.BackupPath); err != nil {
				return false
			}
		}
		if err := writeBinaryHelperOutcomeWithRetry(ctx, staged, binaryOutcomeCancelled); err != nil {
			return false
		}
		c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update helper died before target publication")
		return true
	} else if !os.IsNotExist(err) {
		return false
	}
	return startRecovery()
}

func (c *Coordinator) reconcileBinaryRestartOutcome(ctx context.Context, staged LocalStagedUpdate, generation string) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.RLock()
		active := generation != "" && c.operationGeneration == generation && c.state == StateRestarting
		currentVersion := c.current.Version
		c.mu.RUnlock()
		if !active {
			return
		}
		outcome, err := readBinaryHelperOutcome(staged)
		if err == nil {
			switch {
			case outcome.State == binaryOutcomeSucceeded && currentVersion == staged.Version:
				c.completeGenerationWithRetry(ctx, generation, StateSucceeded, "")
				return
			case outcome.State == binaryOutcomeRolledBack && currentVersion == staged.PreviousVersion:
				c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update rolled back to the prior version")
				return
			case outcome.State == binaryOutcomeCancelled && currentVersion == staged.PreviousVersion:
				c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update helper exited before authorization")
				return
			}
			if c.recoverDeadBinaryHelper(ctx, staged, generation, outcome, currentVersion) {
				return
			}
		} else if os.IsNotExist(err) && staged.OutcomeID != "" && staged.PreviousVersion != "" && currentVersion == staged.PreviousVersion {
			if c.binaryOutcomeReadHook != nil {
				c.binaryOutcomeReadHook()
			}
			prepared, preparedErr := readBinaryHelperPrepared(staged)
			switch {
			case preparedErr == nil && prepared.State == binaryOutcomePrepared:
				// Removing the prepared identity races atomically with the helper's
				// rename claim. Only the winner may classify the handoff.
				if removeErr := os.Remove(binaryHelperPreparedPath(staged.InstallPath)); removeErr == nil {
					c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update helper handoff was not confirmed")
					return
				} else if !os.IsNotExist(removeErr) {
					break
				}
			case os.IsNotExist(preparedErr):
				// The helper may have claimed the prepared identity after the
				// first active-path read. Recheck active after observing prepared
				// absent so a concurrent rename cannot look like no handoff.
				if _, activeErr := readBinaryHelperOutcome(staged); activeErr == nil || !os.IsNotExist(activeErr) {
					break
				}
				// No active or prepared evidence proves the external handoff never began.
				c.completeGenerationWithRetry(ctx, generation, StateRolledBack, "binary update helper handoff did not start")
				return
			}
		}
		timer := time.NewTimer(c.recoveryRetryDelay())
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *Coordinator) StartRecovery(ctx context.Context) {
	c.mu.Lock()
	if ctx != nil {
		c.recoveryCtx = ctx
	}
	recoveryCtx := c.recoveryCtx
	installer, state, generation, failureMessage, release, staged, manual := c.installer, c.state, c.operationGeneration, c.lastError, c.release, c.staged, c.manual
	accepted := c.accepted
	_, dockerAgent := installer.(*DockerAgentInstaller)
	stagedLocal, packagedRestart := staged.(LocalStagedUpdate)
	packagedRestart = packagedRestart && (c.current.Distribution == "binary" || c.current.Distribution == "desktop") && state == StateRestarting
	resumeAcceptance := accepted && generation == "" && (state == StateAvailable || state == StateFailed || state == StateStaging)
	c.mu.Unlock()
	if recoveryCtx == nil {
		return
	}
	needsInstaller := state == StateApplying || state == StateValidating || (state == StateWaitingForIdle && !manual) || (resumeAcceptance && !manual)
	if needsInstaller && installer == nil {
		return
	}
	if needsInstaller || packagedRestart {
		if ready, ok := installer.(recoveryReadyInstaller); ok && !ready.RecoveryReady() {
			return
		}
	}
	if resumeAcceptance {
		c.startAcceptedUpdateSupervisor()
		return
	}
	c.recoveryOnce.Do(func() {
		if packagedRestart && generation != "" {
			go c.reconcileBinaryRestartOutcome(recoveryCtx, stagedLocal, generation)
		} else if state == StateRollingBack && dockerAgent && generation != "" {
			// Docker-agent rollback is unsupported. A crash after the rolling_back
			// transition must only finish exact-generation cleanup; replaying either
			// replacement or rollback would be unsafe after an external side effect.
			go c.completeGenerationWithRetry(recoveryCtx, generation, StateFailed, failureMessage)
		} else if (state == StateWaitingForIdle || (manual && state == StateReady)) && release != nil && generation != "" {
			go c.waitAndApply(recoveryCtx, *release, staged, installer, generation, true)
		} else if resumable, ok := installer.(resumableInstaller); ok && (state == StateApplying || state == StateValidating) && release != nil && generation != "" {
			go c.resumeInstallerUntilSettled(recoveryCtx, resumable, installer, staged, generation, release.Metadata)
		}
	})
}

func (c *Coordinator) StartChecks(ctx context.Context) {
	c.checksOnce.Do(func() {
		go func() {
			_ = c.Check(ctx)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = c.Check(ctx)
				}
			}
		}()
	})
}

func (c *Coordinator) Start(ctx context.Context) {
	c.StartRecovery(ctx)
	c.StartChecks(ctx)
}
