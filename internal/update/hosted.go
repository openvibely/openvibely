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

type HostedDirective struct {
	SchemaVersion     int    `json:"schema_version"`
	Directive         string `json:"directive"`
	UpdateID          string `json:"update_id"`
	DesiredVersion    string `json:"desired_version"`
	Policy            string `json:"policy"`
	DrainLeaseSeconds int    `json:"drain_lease_seconds"`
	ReleaseNotesURL   string `json:"release_notes_url"`
}
type HostedReadyResponse struct {
	SchemaVersion  int       `json:"schema_version"`
	Accepted       bool      `json:"accepted"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}
type HostedLeaseResponse struct {
	SchemaVersion  int       `json:"schema_version"`
	State          string    `json:"state"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}
type hostedPersistentState struct {
	UpdateID, DesiredVersion, Policy, DrainGeneration string
	PreviousVersion                                   string `json:"previous_version"`
	DrainLeaseSeconds                                 int
	LeaseExpiresAt                                    time.Time
	ReleaseNotesURL                                   string
	Phase                                             string
	Error                                             string
	ReadyIdempotencyKey                               string
	LeaseIdempotencyKey                               string
}

const hostedPhaseClaimingReady = "claiming_ready"

type HostedController struct {
	mu                                            sync.Mutex
	api                                           *AgentHTTPClient
	drain                                         *DrainManager
	current                                       CurrentBuild
	statePath                                     string
	stateWriter                                   func(string, []byte) error
	now                                           func() time.Time
	pollInterval, progressInterval, renewInterval time.Duration
	state                                         hostedPersistentState
}

func NewHostedController(api *AgentHTTPClient, drain *DrainManager, current CurrentBuild, statePath string) *HostedController {
	return &HostedController{api: api, drain: drain, current: current, statePath: statePath, now: time.Now, pollInterval: 30 * time.Second, progressInterval: 5 * time.Second, renewInterval: 30 * time.Second}
}
func (c *HostedController) Start(ctx context.Context) {
	go func() {
		c.mu.Lock()
		state := c.state
		c.mu.Unlock()
		if state.Phase == hostedPhaseClaimingReady && c.drain.Owns(state.DrainGeneration) {
			_ = c.reconcileReadinessClaim(ctx, state)
		} else if state.Phase == StateReady && c.drain.Owns(state.DrainGeneration) {
			_ = c.renewUntilReplacement(ctx, state)
		}
		if ctx.Err() == nil {
			c.run(ctx)
		}
	}()
}

func isHostedTerminal(phase string) bool {
	return phase == StateSucceeded || phase == StateRolledBack || phase == StateFailed
}

func (c *HostedController) setPersistentState(state hostedPersistentState) {
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func (c *HostedController) Lifecycle() ManagedUpdateState {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	phase := state.Phase
	if phase == hostedPhaseClaimingReady {
		phase = StateReady
	}
	return ManagedUpdateState{Active: phase != "" && phase != StateIdle, State: phase, DesiredVersion: state.DesiredVersion, ReleaseNotesURL: state.ReleaseNotesURL, Error: state.Error}
}

func (c *HostedController) Restore() error {
	if err := c.load(); err != nil {
		return err
	}
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state.UpdateID == "" {
		status := c.drain.Status()
		if status.State != DrainStateIdle && !c.drain.Release(status.Generation) {
			return errors.New("failed to release orphaned Hosted drain")
		}
		return nil
	}
	status := c.drain.Status()
	if status.State != DrainStateIdle && state.DrainGeneration != status.Generation {
		return errors.New("persisted Hosted update does not own the active drain generation")
	}
	if state.Phase == StateIdle || isHostedTerminal(state.Phase) {
		if status.State != DrainStateIdle && !c.drain.Release(state.DrainGeneration) {
			return errors.New("failed to durably release terminal Hosted drain")
		}
		if state.Phase == StateIdle {
			return c.clear()
		}
		return nil
	}
	if state.Phase == "" {
		if c.drain.Owns(state.DrainGeneration) {
			state.Phase = StateReady
		} else {
			state.Phase = StateWaitingForIdle
		}
		c.setPersistentState(state)
		if err := c.save(); err != nil {
			return err
		}
	}
	if state.Phase == StateReady && c.drain.Owns(state.DrainGeneration) && state.LeaseIdempotencyKey == "" && !state.LeaseExpiresAt.After(c.now()) {
		return c.cancelActive(state)
	}
	if state.DesiredVersion == c.current.Version {
		state.Phase, state.Error = StateSucceeded, ""
		c.setPersistentState(state)
		if err := c.save(); err != nil {
			return err
		}
		if status.State != DrainStateIdle && !c.drain.Release(state.DrainGeneration) {
			return errors.New("persisted Hosted update does not own the active drain generation")
		}
		return nil
	}
	if state.Phase == StateRestarting {
		if !validVersion(state.PreviousVersion) || c.current.Version != state.PreviousVersion {
			return fmt.Errorf("Hosted replacement started an unexpected workspace version %q instead of target %q or prior version %q", c.current.Version, state.DesiredVersion, state.PreviousVersion)
		}
		state.Phase, state.Error = StateRolledBack, "Hosted replacement restarted the prior workspace version"
		c.setPersistentState(state)
		if err := c.save(); err != nil {
			return err
		}
		if status.State != DrainStateIdle && !c.drain.Release(state.DrainGeneration) {
			return errors.New("persisted Hosted rollback does not own the active drain generation")
		}
		return nil
	}
	return nil
}
func (c *HostedController) run(ctx context.Context) {
	backoff := time.Second
	for {
		if err := c.poll(ctx); err != nil {
			if backoff < time.Minute {
				backoff *= 2
			}
		} else {
			backoff = time.Second
		}
		t := time.NewTimer(c.pollInterval + backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
func (c *HostedController) cancelActive(state hostedPersistentState) error {
	state.Phase, state.Error = StateIdle, ""
	c.setPersistentState(state)
	if err := c.save(); err != nil {
		return err
	}
	if state.DrainGeneration != "" {
		status := c.drain.Status()
		if status.State != DrainStateIdle && !c.drain.Release(state.DrainGeneration) {
			return errors.New("failed to durably release cancelled Hosted drain")
		}
	}
	return c.clear()
}

func (c *HostedController) resumeActive(ctx context.Context, active hostedPersistentState) error {
	if active.UpdateID == "" || active.Phase == StateIdle || isHostedTerminal(active.Phase) || active.Phase == StateRestarting {
		return nil
	}
	return c.coordinate(ctx, HostedDirective{
		UpdateID:          active.UpdateID,
		DesiredVersion:    active.DesiredVersion,
		Policy:            active.Policy,
		ReleaseNotesURL:   active.ReleaseNotesURL,
		DrainLeaseSeconds: active.DrainLeaseSeconds,
	}, active)
}

func hostedAssignmentMatches(directive HostedDirective, active hostedPersistentState) bool {
	policy := active.Policy
	if policy == "" {
		policy = "when_idle"
	}
	return directive.DesiredVersion == active.DesiredVersion &&
		directive.Policy == policy &&
		(active.DrainLeaseSeconds == 0 || directive.DrainLeaseSeconds == active.DrainLeaseSeconds) &&
		directive.ReleaseNotesURL == active.ReleaseNotesURL
}

func (c *HostedController) poll(ctx context.Context) error {
	var directive HostedDirective
	err := c.api.Do(ctx, http.MethodGet, "/api/workspace-agent/update-directive", "", nil, &directive, http.StatusOK, http.StatusNoContent)
	if err != nil {
		return err
	}
	c.mu.Lock()
	active := c.state
	c.mu.Unlock()
	if directive.SchemaVersion == 0 {
		return c.resumeActive(ctx, active)
	}
	if directive.SchemaVersion != 1 || directive.UpdateID == "" || (directive.Directive != "update" && directive.Directive != "cancel") {
		return errors.New("invalid hosted update directive")
	}
	if directive.Directive == "cancel" {
		if active.UpdateID == directive.UpdateID && active.Phase != StateRestarting && !isHostedTerminal(active.Phase) {
			return c.cancelActive(active)
		}
		return nil
	}
	if isHostedTerminal(active.Phase) {
		if active.UpdateID == directive.UpdateID {
			return nil
		}
		active = hostedPersistentState{}
		c.setPersistentState(active)
	}
	if active.UpdateID != "" && active.UpdateID != directive.UpdateID {
		return c.decline(ctx, directive.UpdateID, active.DrainGeneration, "drain_failed", "Another assigned update already owns the local drain.")
	}
	if active.UpdateID == directive.UpdateID && !hostedAssignmentMatches(directive, active) {
		return errors.New("hosted update directive conflicts with the durable assignment")
	}
	if directive.DesiredVersion == "" || directive.DrainLeaseSeconds <= 0 {
		return errors.New("invalid hosted update lease")
	}
	if directive.Policy != "when_idle" || !validVersion(directive.DesiredVersion) || compareVersions(directive.DesiredVersion, c.current.Version) <= 0 {
		return c.decline(ctx, directive.UpdateID, active.DrainGeneration, "unsupported_version", "The assigned update policy or desired version is not supported by this workspace.")
	}
	if active.UpdateID == "" {
		status, err := c.drain.BeginDrain(DrainRequest{Lease: time.Duration(directive.DrainLeaseSeconds) * time.Second})
		if err != nil {
			return err
		}
		active = hostedPersistentState{UpdateID: directive.UpdateID, DesiredVersion: directive.DesiredVersion, PreviousVersion: c.current.Version, Policy: directive.Policy, DrainGeneration: status.Generation, DrainLeaseSeconds: directive.DrainLeaseSeconds, LeaseExpiresAt: status.ExpiresAt, ReleaseNotesURL: directive.ReleaseNotesURL, Phase: StateWaitingForIdle}
		c.setPersistentState(active)
		if err := c.save(); err != nil {
			return c.cancelAtOrAfter(ctx, active, c.now(), err)
		}
	}
	return c.coordinate(ctx, directive, active)
}
func (c *HostedController) coordinate(ctx context.Context, directive HostedDirective, active hostedPersistentState) error {
	if active.Phase == StateRestarting {
		return nil
	}
	if active.Phase == StateReady && c.drain.Owns(active.DrainGeneration) {
		return c.renewUntilReplacement(ctx, active)
	}
	progressTicker := time.NewTicker(c.progressInterval)
	defer progressTicker.Stop()
	for {
		status := c.drain.Status()
		if status.State == DrainStateIdle {
			declineErr := c.decline(ctx, active.UpdateID, active.DrainGeneration, "busy_timeout", "The workspace did not become idle before its local deadline.")
			clearErr := c.clear()
			if declineErr != nil || clearErr != nil {
				return errors.Join(declineErr, clearErr)
			}
			return errors.New("hosted drain lease expired")
		}
		if status.State == DrainStateReady {
			if active.ReadyIdempotencyKey == "" {
				active.ReadyIdempotencyKey = randomGeneration()
				active.Phase = hostedPhaseClaimingReady
				c.setPersistentState(active)
				if err := c.save(); err != nil {
					return err
				}
			}
			if !c.drain.Owns(active.DrainGeneration) && !c.drain.TakeOwnership(active.DrainGeneration) {
				return errors.New("hosted readiness claim rejected")
			}
			return c.reconcileReadinessClaim(ctx, active)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-progressTicker.C:
			var next HostedDirective
			if err := c.api.Do(ctx, http.MethodGet, "/api/workspace-agent/update-directive", "", nil, &next, http.StatusOK, http.StatusNoContent); err != nil {
				return err
			}
			if next.SchemaVersion != 0 {
				if next.SchemaVersion != 1 || next.UpdateID == "" || (next.Directive != "update" && next.Directive != "cancel") {
					return errors.New("invalid hosted update directive")
				}
				if next.Directive == "cancel" && next.UpdateID == active.UpdateID {
					return c.cancelActive(active)
				}
				if next.Directive == "update" && (next.UpdateID != active.UpdateID || !hostedAssignmentMatches(next, active)) {
					return errors.New("hosted update directive conflicts with the durable assignment")
				}
			}
			body := struct {
				SchemaVersion   int        `json:"schema_version"`
				DrainGeneration string     `json:"drain_generation"`
				State           string     `json:"state"`
				Active          ActiveWork `json:"active"`
			}{1, active.DrainGeneration, "draining", status.Active}
			if err := c.api.Do(ctx, http.MethodPost, fmt.Sprintf("/api/workspace-agent/updates/%s/progress", directive.UpdateID), randomGeneration(), body, nil, http.StatusNoContent); err != nil {
				return err
			}
		}
	}
}
func (c *HostedController) reconcileReadinessClaim(ctx context.Context, active hostedPersistentState) error {
	body := struct {
		SchemaVersion   int    `json:"schema_version"`
		CurrentVersion  string `json:"current_version"`
		DrainGeneration string `json:"drain_generation"`
		ActiveTotal     int    `json:"active_total"`
	}{1, c.current.Version, active.DrainGeneration, 0}
	for {
		if !c.drain.Owns(active.DrainGeneration) {
			return errors.New("hosted readiness drain generation lost")
		}
		var ready HostedReadyResponse
		err := c.api.Do(ctx, http.MethodPost, fmt.Sprintf("/api/workspace-agent/updates/%s/ready", active.UpdateID), active.ReadyIdempotencyKey, body, &ready, http.StatusAccepted)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil && ready.SchemaVersion == 1 {
			if !ready.Accepted {
				return errors.Join(errors.New("hosted readiness claim rejected"), c.cancelAtOrAfter(ctx, active, c.now(), nil))
			}
			if !ready.LeaseExpiresAt.After(c.now()) {
				return errors.Join(errors.New("hosted readiness claim returned an expired lease"), c.cancelAtOrAfter(ctx, active, c.now(), nil))
			}
			active.LeaseExpiresAt = ready.LeaseExpiresAt
			active.Phase = StateReady
			c.setPersistentState(active)
			if err := c.save(); err != nil {
				return c.persistReadyUntilDurableOrExpiry(ctx, active, err)
			}
			return c.renewUntilReplacement(ctx, active)
		}
		timer := time.NewTimer(c.cancellationRetryInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *HostedController) cancellationRetryInterval() time.Duration {
	if c.renewInterval > 0 && c.renewInterval < time.Second {
		return c.renewInterval
	}
	return time.Second
}

func (c *HostedController) cancelAtOrAfter(ctx context.Context, active hostedPersistentState, notBefore time.Time, cause error) error {
	if remaining := notBefore.Sub(c.now()); remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	for {
		if err := c.cancelActive(active); err == nil {
			return cause
		}
		timer := time.NewTimer(c.cancellationRetryInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *HostedController) saveUntilDurable(ctx context.Context) error {
	for {
		if err := c.save(); err == nil {
			return nil
		}
		timer := time.NewTimer(c.cancellationRetryInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *HostedController) persistReadyUntilDurableOrExpiry(ctx context.Context, active hostedPersistentState, cause error) error {
	if err := c.persistStateUntilDurableOrExpiry(ctx, active, cause); err != nil {
		return err
	}
	return c.renewUntilReplacement(ctx, active)
}

func (c *HostedController) persistStateUntilDurableOrExpiry(ctx context.Context, active hostedPersistentState, cause error) error {
	for {
		remaining := active.LeaseExpiresAt.Sub(c.now())
		if remaining <= 0 {
			return c.cancelAtOrAfter(ctx, active, active.LeaseExpiresAt, cause)
		}
		retry := c.cancellationRetryInterval()
		if retry > remaining {
			retry = remaining
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := c.save(); err == nil {
			return nil
		}
	}
}

func (c *HostedController) renewUntilReplacement(ctx context.Context, active hostedPersistentState) error {
	for {
		if active.LeaseIdempotencyKey != "" {
			var terminal bool
			var err error
			active, terminal, err = c.reconcileLeaseRenewal(ctx, active)
			if err != nil || terminal {
				return err
			}
			continue
		}
		remaining := active.LeaseExpiresAt.Sub(c.now())
		if remaining <= 0 {
			return c.cancelAtOrAfter(ctx, active, active.LeaseExpiresAt, errors.New("hosted readiness lease expired"))
		}
		renewAfter := c.renewInterval
		if renewAfter <= 0 || renewAfter > remaining {
			renewAfter = remaining
		}
		timer := time.NewTimer(renewAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if !active.LeaseExpiresAt.After(c.now()) {
			return c.cancelAtOrAfter(ctx, active, active.LeaseExpiresAt, errors.New("hosted readiness lease expired"))
		}
		active.LeaseIdempotencyKey = randomGeneration()
		c.setPersistentState(active)
		if err := c.save(); err != nil {
			return c.cancelAtOrAfter(ctx, active, active.LeaseExpiresAt, err)
		}
	}
}

func (c *HostedController) reconcileLeaseRenewal(ctx context.Context, active hostedPersistentState) (hostedPersistentState, bool, error) {
	body := struct {
		SchemaVersion   int    `json:"schema_version"`
		DrainGeneration string `json:"drain_generation"`
	}{1, active.DrainGeneration}
	for {
		if !c.drain.Owns(active.DrainGeneration) {
			return active, false, errors.New("hosted renewal drain generation lost")
		}
		var response HostedLeaseResponse
		err := c.api.Do(ctx, http.MethodPost, fmt.Sprintf("/api/workspace-agent/updates/%s/lease", active.UpdateID), active.LeaseIdempotencyKey, body, &response, http.StatusOK)
		if ctx.Err() != nil {
			return active, false, ctx.Err()
		}
		if err != nil || response.SchemaVersion != 1 {
			if err := c.waitForReconciliationRetry(ctx); err != nil {
				return active, false, err
			}
			continue
		}
		switch response.State {
		case "cancelled":
			return active, true, c.cancelAtOrAfter(ctx, active, c.now(), nil)
		case "replacement_started":
			active.Phase = StateRestarting
			active.LeaseIdempotencyKey = ""
			c.setPersistentState(active)
			return active, true, c.saveUntilDurable(ctx)
		case "draining":
			lease := response.LeaseExpiresAt.Sub(c.now())
			if lease <= 0 {
				if err := c.waitForReconciliationRetry(ctx); err != nil {
					return active, false, err
				}
				continue
			}
			if !c.drain.Renew(active.DrainGeneration, lease) {
				if !c.drain.Owns(active.DrainGeneration) {
					return active, false, errors.New("hosted drain generation lost")
				}
				if err := c.waitForReconciliationRetry(ctx); err != nil {
					return active, false, err
				}
				continue
			}
			active.LeaseExpiresAt = response.LeaseExpiresAt
			active.LeaseIdempotencyKey = ""
			c.setPersistentState(active)
			if err := c.save(); err != nil {
				if err := c.persistStateUntilDurableOrExpiry(ctx, active, err); err != nil {
					return active, false, err
				}
			}
			return active, false, nil
		default:
			if err := c.waitForReconciliationRetry(ctx); err != nil {
				return active, false, err
			}
		}
	}
}

func (c *HostedController) waitForReconciliationRetry(ctx context.Context) error {
	timer := time.NewTimer(c.cancellationRetryInterval())
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (c *HostedController) decline(ctx context.Context, id, generation, code, message string) error {
	body := struct {
		SchemaVersion   int    `json:"schema_version"`
		DrainGeneration string `json:"drain_generation"`
		ReasonCode      string `json:"reason_code"`
		Message         string `json:"message"`
	}{1, generation, code, message}
	return c.api.Do(ctx, http.MethodPost, fmt.Sprintf("/api/workspace-agent/updates/%s/decline", id), randomGeneration(), body, nil, http.StatusNoContent)
}
func (c *HostedController) save() error {
	c.mu.Lock()
	state := c.state
	writer := c.stateWriter
	c.mu.Unlock()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if writer == nil {
		writer = atomicWriteState
	}
	return writer(c.statePath, data)
}
func (c *HostedController) load() error {
	data, err := os.ReadFile(c.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.Unmarshal(data, &c.state)
}
func (c *HostedController) clear() error {
	if err := os.Remove(c.statePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	c.mu.Lock()
	c.state = hostedPersistentState{}
	c.mu.Unlock()
	return nil
}
