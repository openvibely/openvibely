package service

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputerHumanFilesWithoutTakeover(t *testing.T) {
	calls := 0
	_, s, p, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case r.Method == http.MethodPut:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "upload", string(body))
			io.WriteString(w, `{}`)
		case r.URL.Query().Get("path") == "Files/report.txt":
			w.Header().Set("Content-Type", "application/octet-stream")
			io.WriteString(w, "report")
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"entries":[]}`)
		}
	})
	state, err := s.Get(t.Context(), p.ID)
	require.NoError(t, err)
	require.False(t, state.Held)
	_, _, err = s.HumanFiles(t.Context(), p.ID, "", nil, false)
	require.NoError(t, err)
	data, kind, err := s.HumanFiles(t.Context(), p.ID, "report.txt", nil, false)
	require.NoError(t, err)
	require.Equal(t, "report", string(data))
	require.Equal(t, "application/octet-stream", kind)
	_, _, err = s.HumanFiles(t.Context(), p.ID, "upload.txt", []byte("upload"), true)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	_, _, err = s.HumanFiles(t.Context(), p.ID, "../outside", nil, false)
	require.Error(t, err)
	require.NoError(t, s.repo.SetTakeoverPending(t.Context(), p.ID, true))
	_, _, err = s.HumanFiles(t.Context(), p.ID, "", nil, false)
	require.Error(t, err)
	require.NoError(t, s.repo.SetTakeoverPending(t.Context(), p.ID, false))
	require.NoError(t, s.SetTaskEnabled(t.Context(), p.ID, false))
	_, _, err = s.HumanFiles(t.Context(), p.ID, "", nil, false)
	require.Error(t, err)
	require.Equal(t, 3, calls, "rejected operations must not reach the computer")
}
