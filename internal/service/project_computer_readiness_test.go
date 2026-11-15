package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

type blockingReadinessProvisioner struct {
	fakeComputerProvisioner
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (p *blockingReadinessProvisioner) ResolveCurrent(ctx context.Context, id string) (ComputerConnection, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-ctx.Done():
		return ComputerConnection{}, ctx.Err()
	case <-p.release:
	}
	return ComputerConnection{Endpoint: "http://computer"}, nil
}
func TestComputerReadinessSharesOnlyOverlappingContainerLookups(t *testing.T) {
	p := &blockingReadinessProvisioner{entered: make(chan struct{}), release: make(chan struct{})}
	svc := &ProjectComputerService{provisioner: p, readinessLookups: &singleflight.Group{}}
	done := make(chan error, 1)
	go func() { _, err := svc.resolveConnection(t.Context(), "project", true); done <- err }()
	<-p.entered
	// A second caller joins the running lookup, but can cancel independently.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.resolveConnection(ctx, "project", true)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, p.calls.Load())
	close(p.release)
	require.NoError(t, <-done)
	// Completion is not a cache: the next request must inspect again.
	conn, err := svc.resolveConnection(t.Context(), "project", true)
	require.NoError(t, err)
	require.Equal(t, "http://computer", conn.Endpoint)
	require.EqualValues(t, 2, p.calls.Load())
}
