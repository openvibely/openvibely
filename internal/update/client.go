package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"golang.org/x/mod/semver"
)

const (
	checkSchemaVersion = 1
	installIDOptOutEnv = "OPENVIBELY_DISABLE_INSTALL_ID"
	// The server reports 30-day actives, so rotation must be at least that long;
	// 90 days prevents one install from rotating mid-window and being counted twice.
	installIDRotationWindow = 90 * 24 * time.Hour
)

type CurrentBuild struct {
	buildinfo.Build
	Distribution string
}
type CheckRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	Distribution  string `json:"distribution"`
	Channel       string `json:"channel"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	InstallID     string `json:"install_id,omitempty"`
}
type CheckResponse struct {
	SchemaVersion    int            `json:"schema_version"`
	UpdateAvailable  bool           `json:"update_available"`
	LatestVersion    string         `json:"latest_version"`
	Channel          string         `json:"channel"`
	ApplySupported   bool           `json:"apply_supported"`
	Action           string         `json:"action"`
	ReleaseNotesURL  string         `json:"release_notes_url"`
	Message          string         `json:"message"`
	SelectedTargetID string         `json:"selected_target_id"`
	Release          *SignedRelease `json:"release"`
}
type SignedRelease struct {
	Signed     json.RawMessage `json:"signed"`
	Signatures []Signature     `json:"signatures"`
}
type Signature struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}
type ReleaseMetadata struct {
	SchemaVersion         int       `json:"schema_version"`
	Version               string    `json:"version"`
	Commit                string    `json:"commit"`
	Channel               string    `json:"channel"`
	PublishedAt           time.Time `json:"published_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	ReleaseNotesURL       string    `json:"release_notes_url"`
	MinimumUpdaterVersion string    `json:"minimum_updater_version"`
	MinimumHostedVersion  string    `json:"minimum_hosted_version"`
	DatabaseCompatibility string    `json:"database_compatibility"`
	Targets               []Target  `json:"targets"`
}
type Target struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url,omitempty"`
	Filename string `json:"filename,omitempty"`
	Filetype string `json:"filetype,omitempty"`
	Size     int64  `json:"size,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	ImageRef string `json:"image_ref,omitempty"`
}
type VerifiedRelease struct {
	Metadata       ReleaseMetadata `json:"metadata"`
	Target         Target          `json:"target"`
	ApplySupported bool            `json:"apply_supported"`
	Action         string          `json:"action"`
}
type VerifiedArtifact struct {
	Path    string
	Target  Target
	Release ReleaseMetadata
}

type persistedClientState struct {
	LastSuccessfulCheck    time.Time        `json:"last_successful_check"`
	HighestAcceptedVersion string           `json:"highest_accepted_version,omitempty"`
	MetadataExpiresAt      time.Time        `json:"metadata_expires_at,omitempty"`
	Cached                 *VerifiedRelease `json:"cached,omitempty"`
	Failures               int              `json:"failures,omitempty"`
	NextAttempt            time.Time        `json:"next_attempt,omitempty"`
	InstallID              string           `json:"install_id,omitempty"`
	InstallIDIssuedAt      time.Time        `json:"install_id_issued_at,omitempty"`
}

func (s persistedClientState) MarshalJSON() ([]byte, error) {
	type stateJSON persistedClientState
	var installIDIssuedAt *time.Time
	if !s.InstallIDIssuedAt.IsZero() {
		installIDIssuedAt = &s.InstallIDIssuedAt
	}
	return json.Marshal(struct {
		stateJSON
		InstallIDIssuedAt *time.Time `json:"install_id_issued_at,omitempty"`
	}{stateJSON: stateJSON(s), InstallIDIssuedAt: installIDIssuedAt})
}

type ClientConfig struct {
	ServiceURL, Channel, StatePath string
	HTTPClient                     *http.Client
	PublicKeys                     map[string]ed25519.PublicKey
	Now                            func() time.Time
	Random                         func(time.Duration) time.Duration
}
type Client struct{ cfg ClientConfig }

func DecodePublicKeys(embeddedID, embeddedBase64, filePath string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	if strings.TrimSpace(embeddedBase64) != "" {
		raw, err := base64.StdEncoding.DecodeString(embeddedBase64)
		if err != nil || len(raw) != ed25519.PublicKeySize || embeddedID == "" {
			return nil, errors.New("invalid embedded release public key")
		}
		keys[embeddedID] = ed25519.PublicKey(raw)
	}
	if strings.TrimSpace(filePath) != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}
		var encoded map[string]string
		if err := json.Unmarshal(data, &encoded); err != nil {
			return nil, errors.New("update public key file must be a JSON object of key IDs to base64 Ed25519 keys")
		}
		for id, value := range encoded {
			if _, embedded := keys[id]; embedded {
				return nil, fmt.Errorf("configured update public key %q conflicts with the embedded release key", id)
			}
			raw, err := base64.StdEncoding.DecodeString(value)
			if err != nil || len(raw) != ed25519.PublicKeySize || id == "" {
				return nil, fmt.Errorf("invalid Ed25519 public key %q", id)
			}
			keys[id] = ed25519.PublicKey(raw)
		}
	}
	return keys, nil
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = func(max time.Duration) time.Duration {
			if max <= 0 {
				return 0
			}
			return time.Duration(cfg.Now().UnixNano() % int64(max))
		}
	}
	base := cfg.HTTPClient
	clone := *base
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
			return errors.New("cross-origin update API redirect rejected")
		}
		if base.CheckRedirect != nil {
			return base.CheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	cfg.HTTPClient = &clone
	return &Client{cfg: cfg}
}

func (c *Client) CheckIfDue(ctx context.Context, current CurrentBuild) (*VerifiedRelease, bool, error) {
	state, err := c.loadState()
	if err != nil {
		return nil, false, fmt.Errorf("load update rollback-protection state: %w", err)
	}
	now := c.cfg.Now()
	if !state.LastSuccessfulCheck.IsZero() && now.Sub(state.LastSuccessfulCheck) < 24*time.Hour {
		return state.Cached, false, nil
	}
	if now.Before(state.NextAttempt) {
		return state.Cached, false, nil
	}

	installIDPersistenceFailed := false
	if _, optedOut := os.LookupEnv(installIDOptOutEnv); optedOut {
		state.InstallID = ""
		state.InstallIDIssuedAt = time.Time{}
	} else if state.InstallID == "" || state.InstallIDIssuedAt.Before(now.Add(-installIDRotationWindow)) {
		installID, err := generateInstallID()
		if err != nil {
			return state.Cached, true, err
		}
		state.InstallID = installID
		state.InstallIDIssuedAt = now
		if err := c.saveState(state); err != nil {
			installIDPersistenceFailed = true
		}
	}

	requestBody := CheckRequest{
		SchemaVersion: checkSchemaVersion,
		Version:       current.Version,
		Commit:        current.Commit,
		Distribution:  current.Distribution,
		Channel:       c.cfg.Channel,
		OS:            current.OS,
		Arch:          current.Arch,
		InstallID:     state.InstallID,
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return nil, true, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.ServiceURL, "/")+"/api/updates/check", bytes.NewReader(encoded))
	if err != nil {
		return nil, true, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		c.recordFailure(&state, now)
		return state.Cached, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.recordFailure(&state, now)
		return state.Cached, true, fmt.Errorf("update check returned HTTP %d", resp.StatusCode)
	}
	var response CheckResponse
	limited := io.LimitReader(resp.Body, (4<<20)+1)
	data, readErr := io.ReadAll(limited)
	if readErr != nil || len(data) > 4<<20 {
		c.recordFailure(&state, now)
		if readErr != nil {
			return state.Cached, true, readErr
		}
		return state.Cached, true, errors.New("update response exceeds size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&response); err != nil {
		c.recordFailure(&state, now)
		return state.Cached, true, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		c.recordFailure(&state, now)
		return state.Cached, true, errors.New("update response contains trailing JSON")
	}
	if response.SchemaVersion != checkSchemaVersion {
		c.recordFailure(&state, now)
		return state.Cached, true, fmt.Errorf("unsupported update schema_version %d", response.SchemaVersion)
	}
	if current.Distribution == buildinfo.DistributionSource {
		state.LastSuccessfulCheck, state.Failures, state.NextAttempt = now, 0, now.Add(24*time.Hour+c.cfg.Random(time.Hour))
		state.Cached = nil
		saveErr := c.saveState(state)
		if installIDPersistenceFailed {
			saveErr = nil
		}
		return nil, true, saveErr
	}
	var verified *VerifiedRelease
	if response.Release != nil {
		verified, err = c.verifyRelease(response, current, state.HighestAcceptedVersion, now)
		if err != nil {
			c.recordFailure(&state, now)
			return state.Cached, true, err
		}
		state.HighestAcceptedVersion, state.MetadataExpiresAt, state.Cached = verified.Metadata.Version, verified.Metadata.ExpiresAt, verified
	} else {
		state.Cached = nil
	}
	state.LastSuccessfulCheck, state.Failures, state.NextAttempt = now, 0, now.Add(24*time.Hour+c.cfg.Random(time.Hour))
	saveErr := c.saveState(state)
	if installIDPersistenceFailed {
		saveErr = nil
	}
	return verified, true, saveErr
}

func (c *Client) verifyRelease(response CheckResponse, current CurrentBuild, highest string, now time.Time) (*VerifiedRelease, error) {
	if response.Release == nil || len(response.Release.Signatures) == 0 {
		return nil, errors.New("signed release required")
	}
	canonical, err := canonicalJSON(response.Release.Signed)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing signed release: %w", err)
	}
	valid := false
	for _, sig := range response.Release.Signatures {
		key := c.cfg.PublicKeys[sig.KeyID]
		raw, decodeErr := base64.StdEncoding.DecodeString(sig.Value)
		if sig.Algorithm == "ed25519" && len(key) == ed25519.PublicKeySize && decodeErr == nil && ed25519.Verify(key, canonical, raw) {
			valid = true
			break
		}
	}
	if !valid {
		return nil, errors.New("release signature verification failed")
	}
	var metadata ReleaseMetadata
	if err := json.Unmarshal(response.Release.Signed, &metadata); err != nil {
		return nil, err
	}
	if metadata.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported release schema_version %d", metadata.SchemaVersion)
	}
	if !validVersion(metadata.Version) || metadata.MinimumUpdaterVersion != "" && !validVersion(metadata.MinimumUpdaterVersion) {
		return nil, errors.New("release contains an invalid semantic version")
	}
	if !metadata.ExpiresAt.After(now) {
		return nil, errors.New("release metadata expired")
	}
	if metadata.Channel != c.cfg.Channel || response.Channel != "" && response.Channel != metadata.Channel {
		return nil, errors.New("release channel mismatch")
	}
	if response.LatestVersion != "" && response.LatestVersion != metadata.Version {
		return nil, errors.New("release version hint does not match signed metadata")
	}
	if response.ReleaseNotesURL != "" && response.ReleaseNotesURL != metadata.ReleaseNotesURL {
		return nil, errors.New("release notes hint does not match signed metadata")
	}
	manualOnly := metadata.MinimumUpdaterVersion != "" && compareVersions(metadata.MinimumUpdaterVersion, "0.1.0") > 0 || !response.ApplySupported || response.Action == "manual"
	if compareVersions(metadata.Version, current.Version) <= 0 || highest != "" && compareVersions(metadata.Version, highest) < 0 {
		return nil, errors.New("release version is not an authorized upgrade")
	}
	if manualOnly {
		if response.ApplySupported || response.Action != "manual" || response.SelectedTargetID != "" {
			return nil, errors.New("manual update response has inconsistent routing hints")
		}
		return &VerifiedRelease{Metadata: metadata, ApplySupported: false, Action: "manual"}, nil
	}
	if !response.ApplySupported {
		return nil, errors.New("automatic update response must set apply_supported")
	}
	expectedAction := "download"
	if current.Distribution == buildinfo.DistributionDocker || current.Distribution == buildinfo.DistributionHosted {
		expectedAction = "container"
	}
	if response.Action != expectedAction {
		return nil, errors.New("automatic update action does not match the verified distribution")
	}
	var selected *Target
	for i := range metadata.Targets {
		if metadata.Targets[i].ID == response.SelectedTargetID {
			selected = &metadata.Targets[i]
			break
		}
	}
	if response.SelectedTargetID == "" || selected == nil {
		return nil, errors.New("selected release target mismatch")
	}
	if selected.OS != current.OS || selected.Arch != current.Arch && selected.Arch != "multi" {
		return nil, errors.New("selected release target platform mismatch")
	}
	switch current.Distribution {
	case buildinfo.DistributionDesktop:
		if selected.Kind != "app_bundle" {
			return nil, errors.New("desktop release target must be an app bundle")
		}
	case buildinfo.DistributionBinary:
		if selected.Kind != "binary" && selected.Kind != "executable" {
			return nil, errors.New("binary release target must be an executable")
		}
	case buildinfo.DistributionDocker, buildinfo.DistributionHosted:
		if selected.Kind != "oci" {
			return nil, errors.New("container release target must be OCI")
		}
	}
	if selected.Kind == "oci" {
		if !validOCIImageRef(selected.ImageRef) {
			return nil, errors.New("OCI target must use an exact SHA-256 digest reference")
		}
	} else if selected.Size <= 0 || len(selected.SHA256) != 64 || selected.URL == "" {
		return nil, errors.New("download target has invalid size, digest, or URL")
	} else if artifactURL, err := url.Parse(selected.URL); err != nil || !artifactURL.IsAbs() || artifactURL.User != nil || artifactURL.Fragment != "" || (artifactURL.Scheme != "https" && !(artifactURL.Scheme == "http" && isLoopbackHost(artifactURL.Hostname()))) {
		return nil, errors.New("download target URL must use HTTPS or loopback HTTP")
	}
	return &VerifiedRelease{Metadata: metadata, Target: *selected, ApplySupported: true, Action: expectedAction}, nil
}

var ociImageRefPattern = regexp.MustCompile(`^(?:(?:localhost|[a-z0-9]+(?:[.-][a-z0-9]+)*)(?::[0-9]+)?/)?[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[a-f0-9]{64}$`)

func validOCIImageRef(imageRef string) bool {
	if !ociImageRefPattern.MatchString(imageRef) {
		return false
	}
	nameEnd := strings.LastIndexByte(imageRef, '@')
	return nameEnd > 0 && nameEnd <= 255
}

func (c *Client) ValidateForInstall(release VerifiedRelease, current CurrentBuild) error {
	now := c.cfg.Now()
	if !release.Metadata.ExpiresAt.After(now) {
		return errors.New("release metadata expired")
	}
	if release.Metadata.Channel != c.cfg.Channel {
		return errors.New("release channel mismatch")
	}
	if release.Metadata.MinimumUpdaterVersion != "" && compareVersions(release.Metadata.MinimumUpdaterVersion, "0.1.0") > 0 {
		return errors.New("release requires a newer updater and is manual-only")
	}
	if release.Action == "manual" {
		return errors.New("release is manual-only")
	}
	if compareVersions(release.Metadata.Version, current.Version) <= 0 {
		return errors.New("release version is not an authorized upgrade")
	}
	state, err := c.loadState()
	if err != nil {
		return err
	}
	if state.HighestAcceptedVersion != "" && compareVersions(release.Metadata.Version, state.HighestAcceptedVersion) < 0 {
		return errors.New("release version is below the highest accepted version")
	}
	if !state.MetadataExpiresAt.IsZero() && !state.MetadataExpiresAt.After(now) {
		return errors.New("cached release metadata expired")
	}
	return nil
}

var (
	errArtifactRedirectPolicy   = errors.New("artifact redirect policy rejected")
	errArtifactDestinationWrite = errors.New("artifact destination write failed")
)

type artifactDestinationWriter struct {
	destination io.Writer
	digest      hash.Hash
}

func (w artifactDestinationWriter) Write(p []byte) (int, error) {
	n, err := w.destination.Write(p)
	if n > 0 {
		_, _ = w.digest.Write(p[:n])
	}
	if err != nil {
		return n, errors.Join(errArtifactDestinationWrite, err)
	}
	if n != len(p) {
		return n, errors.Join(errArtifactDestinationWrite, io.ErrShortWrite)
	}
	return n, nil
}

func copyArtifactPayload(destination io.Writer, source io.Reader, size int64) (int64, string, error) {
	digest := sha256.New()
	written, err := io.Copy(artifactDestinationWriter{destination: destination, digest: digest}, io.LimitReader(source, size+1))
	encodedDigest := hex.EncodeToString(digest.Sum(nil))
	if err == nil || errors.Is(err, errArtifactDestinationWrite) {
		return written, encodedDigest, err
	}
	return written, encodedDigest, errors.Join(ErrUpdateRetryable, err)
}

func (c *Client) Download(ctx context.Context, release VerifiedRelease, dst io.Writer, progress func(int64, int64)) error {
	if !release.Metadata.ExpiresAt.After(c.cfg.Now()) {
		return errors.New("release metadata expired")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.Target.URL, nil)
	if err != nil {
		return err
	}
	client := *c.cfg.HTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("%w: too many redirects", errArtifactRedirectPolicy)
		}
		req.Header.Del("Authorization")
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errArtifactRedirectPolicy) {
			return err
		}
		return errors.Join(ErrUpdateRetryable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("artifact returned HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return errors.Join(ErrUpdateRetryable, err)
		}
		return err
	}
	written, digest, err := copyArtifactPayload(dst, resp.Body, release.Target.Size)
	if progress != nil {
		progress(written, release.Target.Size)
	}
	if err != nil {
		return err
	}
	if written != release.Target.Size {
		return errors.New("artifact size mismatch")
	}
	if digest != strings.ToLower(release.Target.SHA256) {
		return errors.New("artifact SHA-256 mismatch")
	}
	return nil
}

func (c *Client) Fetch(ctx context.Context, release VerifiedRelease, destination string) (*VerifiedArtifact, error) {
	if release.Target.Kind == "oci" {
		return nil, errors.New("OCI targets are verified and installed by the container controller")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.Target.URL, nil)
	if err != nil {
		return nil, err
	}
	client := *c.cfg.HTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("%w: too many redirects", errArtifactRedirectPolicy)
		}
		req.Header.Del("Authorization")
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errArtifactRedirectPolicy) {
			return nil, err
		}
		return nil, errors.Join(ErrUpdateRetryable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("artifact returned HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooEarly || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, errors.Join(ErrUpdateRetryable, err)
		}
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	written, digest, copyErr := copyArtifactPayload(f, resp.Body, release.Target.Size)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return nil, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return nil, closeErr
	}
	if written != release.Target.Size {
		_ = os.Remove(destination)
		return nil, errors.New("artifact size mismatch")
	}
	if digest != strings.ToLower(release.Target.SHA256) {
		_ = os.Remove(destination)
		return nil, errors.New("artifact SHA-256 mismatch")
	}
	return &VerifiedArtifact{Path: destination, Target: release.Target, Release: release.Metadata}, nil
}

func generateInstallID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (c *Client) recordFailure(state *persistedClientState, now time.Time) {
	state.Failures++
	power := math.Min(float64(state.Failures-1), 6)
	delay := time.Duration(math.Pow(2, power)) * time.Minute
	state.NextAttempt = now.Add(delay + c.cfg.Random(delay/4))
	_ = c.saveState(*state)
}
func (c *Client) loadState() (persistedClientState, error) {
	var s persistedClientState
	b, err := os.ReadFile(c.cfg.StatePath)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}
func (c *Client) saveState(s persistedClientState) error {
	if err := os.MkdirAll(filepath.Dir(c.cfg.StatePath), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := c.cfg.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.cfg.StatePath)
}
func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}
func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}
func validVersion(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	return semver.IsValid(value)
}
func compareVersions(a, b string) int {
	normalize := func(value string) string {
		if strings.HasPrefix(value, "v") {
			return value
		}
		return "v" + value
	}
	a, b = normalize(strings.TrimSpace(a)), normalize(strings.TrimSpace(b))
	if !semver.IsValid(a) || !semver.IsValid(b) {
		return 0
	}
	return semver.Compare(a, b)
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	return jsoncanonicalizer.Transform(raw)
}
