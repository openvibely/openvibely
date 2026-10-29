package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
)

// configureAcceptedOffer supplies the signed authority used at the install boundary.
func configureAcceptedOffer(t *testing.T, c *Coordinator) *atomic.Int32 {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c.current.OS, c.current.Arch = "linux", "amd64"
	target := Target{ID: "linux-amd64", Kind: "executable", OS: "linux", Arch: "amd64", URL: "https://example.com/app", Size: 3, SHA256: strings.Repeat("a", 64)}
	c.release.Metadata.SchemaVersion = 1
	c.release.Metadata.Targets = []Target{target}
	c.release.Target, c.release.ApplySupported, c.release.Action = target, true, "download"
	response := signedCheckResponse(t, private, c.release.Metadata)
	changed := c.release.Metadata
	changed.Targets = append([]Target(nil), changed.Targets...)
	changed.Targets[0].SHA256 = strings.Repeat("b", 64)
	changedResponse := signedCheckResponse(t, private, changed)
	lower := c.release.Metadata
	lower.Version = "0.5.5"
	lowerResponse := signedCheckResponse(t, private, lower)
	mode := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			_ = json.NewEncoder(w).Encode(CheckResponse{SchemaVersion: 1})
		case 2:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case 3:
			_ = json.NewEncoder(w).Encode(changedResponse)
		case 4:
			_ = json.NewEncoder(w).Encode(lowerResponse)
		default:
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	t.Cleanup(server.Close)
	c.client.cfg.ServiceURL = server.URL
	c.client.cfg.PublicKeys = map[string]ed25519.PublicKey{"release": public}
	return mode
}

func TestAcceptedUpdateWithdrawalWhileDraining(t *testing.T) {
	now := time.Now()
	tracker := NewWorkTracker()
	done, err := tracker.Start(WorkTask)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	drain := NewDrainManager(tracker.Active, nil, 0, time.Now)
	drain.SetWorkTracker(tracker)
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
	installer := &countingInstaller{}
	c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", drain, installer, false, "", nil)
	c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)}}
	mode := configureAcceptedOffer(t, c)
	c.staged, c.state = *c.release, StateAvailable
	// Seed today's discovery cache. Withdrawal must bypass that cache.
	if _, _, err := client.CheckIfDue(context.Background(), c.current); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.StartRecovery(ctx)
	if err := c.Accept(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	waitForCoordinatorState(t, c, StateWaitingForIdle)
	mode.Store(1)
	done()
	waitForCoordinatorState(t, c, StateFailed)
	if installer.applies.Load() != 0 {
		t.Fatal("withdrawn release installed")
	}
	if !tracker.Admit() {
		t.Fatal("withdrawal left tasks blocked")
	}
	if !strings.Contains(c.Snapshot().Error, ErrUpdateOfferChanged.Error()) {
		t.Fatalf("missing withdrawal diagnostic: %#v", c.Snapshot())
	}
}

func TestValidateOfferNeverFallsBackToCache(t *testing.T) {
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
	c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", nil, nil, false, "", nil)
	c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: time.Now().Add(time.Hour)}}
	mode := configureAcceptedOffer(t, c)
	if err := client.ValidateOffer(context.Background(), *c.release, c.current); err != nil {
		t.Fatal(err)
	}
	mode.Store(2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.ValidateOffer(ctx, *c.release, c.current); err == nil || errors.Is(err, ErrUpdateOfferChanged) {
		t.Fatalf("outage must fail without declaring withdrawal: %v", err)
	}
	mode.Store(1)
	if err := client.ValidateOffer(context.Background(), *c.release, c.current); !errors.Is(err, ErrUpdateOfferChanged) {
		t.Fatalf("withdrawal ignored after network backoff: %v", err)
	}
}

func TestValidateOfferRejectsChangedTargetAndLowerPromotion(t *testing.T) {
	for _, modeValue := range []int32{3, 4} {
		client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(t.TempDir(), "client.json")})
		c := NewCoordinator(client, CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}, Distribution: buildinfo.DistributionBinary}, "stable", nil, nil, false, "", nil)
		c.release = &VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: time.Now().Add(time.Hour)}}
		mode := configureAcceptedOffer(t, c)
		if err := client.ValidateOffer(context.Background(), *c.release, c.current); err != nil {
			t.Fatal(err)
		}
		mode.Store(modeValue)
		if err := client.ValidateOffer(context.Background(), *c.release, c.current); !errors.Is(err, ErrUpdateOfferChanged) {
			t.Fatalf("mode %d: changed offer accepted: %v", modeValue, err)
		}
		state, err := client.loadState()
		if err != nil {
			t.Fatal(err)
		}
		if state.HighestAcceptedVersion != "0.6.0" {
			t.Fatalf("rollback floor changed: %s", state.HighestAcceptedVersion)
		}
	}
}
