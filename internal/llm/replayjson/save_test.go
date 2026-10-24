package replayjson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type countedHistory struct {
	calls int
	text  string
	err   error
}

func (h *countedHistory) MarshalJSON() ([]byte, error) {
	h.calls++
	if h.err != nil {
		return nil, h.err
	}
	return json.Marshal(h.text)
}

func TestSaverEncodesOnceAndNotifiesAfterPersistence(t *testing.T) {
	history := &countedHistory{text: "first"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var persisted string
	var events []string
	saver := Saver{
		Persist: func(got context.Context, raw string) error {
			require.Same(t, ctx, got, "the provider controls cancellation policy")
			require.ErrorIs(t, got.Err(), context.Canceled)
			require.Equal(t, 1, history.calls)
			events = append(events, "persist")
			persisted = raw
			return nil
		},
		AfterSave: func(raw string) {
			events = append(events, "recover")
			require.Equal(t, persisted, raw)
			require.Equal(t, 1, history.calls)
		},
	}
	require.NoError(t, saver.Save(ctx, history))
	require.Equal(t, []string{"persist", "recover"}, events)
	require.Equal(t, `"first"`, persisted)
}

func TestSaverFailuresAndRetry(t *testing.T) {
	encodeFailure := errors.New("encoding failure")
	writeFailure := errors.New("write failure")
	history := &countedHistory{text: "value", err: encodeFailure}
	writes, recovered := 0, 0
	failWrite := true
	saver := Saver{
		Persist: func(ctx context.Context, raw string) error {
			writes++
			if failWrite {
				return writeFailure
			}
			require.Equal(t, `"changed"`, raw)
			return nil
		},
		AfterSave:   func(string) { recovered++ },
		EncodeError: func(err error) error { return fmt.Errorf("provider encoding: %w", err) },
	}
	err := saver.Save(context.Background(), history)
	require.ErrorIs(t, err, encodeFailure)
	require.Contains(t, err.Error(), "provider encoding")
	require.Zero(t, writes)
	require.Zero(t, recovered)
	history.err = nil
	require.ErrorIs(t, saver.Save(context.Background(), history), writeFailure)
	require.Zero(t, recovered)
	failWrite = false
	history.text = "changed"
	require.NoError(t, saver.Save(context.Background(), history))
	require.Equal(t, 3, history.calls)
	require.Equal(t, 2, writes)
	require.Equal(t, 1, recovered)
	// Another save still writes; the helper neither caches nor suppresses retries.
	saver.AfterSave = nil
	require.NoError(t, saver.Save(context.Background(), history))
	require.Equal(t, 4, history.calls)
	require.Equal(t, 3, writes)
	require.Equal(t, 1, recovered)
}
