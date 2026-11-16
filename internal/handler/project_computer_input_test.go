package handler

import (
	"bytes"
	"context"
	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdaptiveRFBFiltersFragmentedInput(t *testing.T) {
	handshake := append([]byte("RFB 003.008\n"), 1, 1)
	display := []byte{3, 1, 0, 0, 0, 0, 0, 10, 0, 10}
	inputs := [][]byte{
		{4, 1, 0, 0, 0, 0, 0, 65},
		{5, 1, 0, 1, 0, 2},
		{5, 128, 0, 1, 0, 2, 1},
		{6, 0, 0, 0, 0, 0, 0, 3, 'a', 'b', 'c'},
		{6, 0, 0, 0, 255, 255, 255, 252, 1, 0, 0, 1},
		{255, 0, 0, 1, 0, 0, 0, 65, 0, 0, 0, 30},
		{251, 0, 0, 10, 0, 10, 0, 0},
	}
	for _, enabled := range []bool{false, true} {
		for split := 1; split <= 64; split++ {
			var parser adaptiveRFB
			var got bytes.Buffer
			write := func(b []byte) error { _, err := got.Write(b); return err }
			source := bytes.Clone(handshake)
			source = append(source, display...)
			for _, msg := range inputs {
				source = append(source, msg...)
			}
			source = append(source, display...)
			for i := 0; i < len(source); i += split {
				end := min(i+split, len(source))
				require.NoError(t, parser.consume(source[i:end], enabled, write))
			}
			want := source
			if !enabled {
				want = bytes.Clone(handshake)
				want = append(want, display...)
				want = append(want, display...)
			}
			require.Equal(t, want, got.Bytes())
			got.Reset()
			require.NoError(t, parser.release(write))
			if enabled {
				require.Contains(t, string(got.Bytes()), string([]byte{4, 0, 0, 0, 0, 0, 0, 65}))
				require.Contains(t, string(got.Bytes()), string([]byte{5, 0, 0, 1, 0, 2}))
			} else {
				require.Empty(t, got.Bytes())
			}
		}
	}
}
func TestAdaptiveRFBRejectsUnknownAndOversizedMessages(t *testing.T) {
	for _, data := range [][]byte{{1}, {255, 1}, {6, 0, 0, 0, 127, 255, 255, 255}, {248, 0, 0, 0, 0, 0, 0, 0, 65}} {
		parser := adaptiveRFB{stage: 3}
		called := false
		require.Error(t, parser.consume(data, false, func([]byte) error { called = true; return nil }))
		require.False(t, called)
	}
	p := adaptiveRFB{}
	require.Error(t, p.consume([]byte("RFB 003.003\n"), false, func([]byte) error { return nil }))
	p = adaptiveRFB{stage: 1}
	require.Error(t, p.consume([]byte{2}, false, func([]byte) error { return nil }))
	p = adaptiveRFB{stage: 2}
	require.Error(t, p.consume([]byte{0}, false, func([]byte) error { return nil }), "viewers must not disconnect others with an exclusive session")
}

// Echoing the accepted protocol bytes makes each display request a barrier:
// blocked input cannot hide behind a timing-based negative assertion.
func TestAdaptiveStreamSwitchesInputWithoutReconnecting(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			kind, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if c.Write(r.Context(), kind, b) != nil {
				return
			}
		}
	}))
	defer bridge.Close()
	db := teststores.New(t)
	project := &models.Project{Name: "Adaptive fixture"}
	require.NoError(t, db.Projects.Create(ctx, project))
	require.NoError(t, db.Projects.Computers().Configure(ctx, project.ID, "local"))
	require.NoError(t, db.Projects.Computers().SetEnabled(ctx, project.ID, true))
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL, Token: "fixture"}})
	h := &Handler{projectRepo: db.Projects, projectSvc: service.NewProjectService(db.Projects), computerSvc: svc}
	e := echo.New()
	e.GET("/projects/:id/stream/:mode", h.ProjectComputerStream)
	server := httptest.NewServer(e)
	defer server.Close()
	c, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/projects/"+project.ID+"/stream/adaptive", &websocket.DialOptions{Subprotocols: []string{"binary"}})
	require.NoError(t, err)
	defer c.CloseNow()
	send := func(b []byte) { require.NoError(t, c.Write(ctx, websocket.MessageBinary, b)) }
	read := func(want []byte) {
		kind, b, err := c.Read(ctx)
		require.NoError(t, err)
		require.Equal(t, websocket.MessageBinary, kind)
		require.Equal(t, want, b)
	}
	send(append([]byte("RFB 003.008\n"), 1, 1))
	read([]byte("RFB 003.008\n"))
	read([]byte{1})
	read([]byte{1})
	key := []byte{4, 1, 0, 0, 0, 0, 0, 65}
	display := []byte{3, 1, 0, 0, 0, 0, 0, 10, 0, 10}
	send(append(bytes.Clone(key), display...))
	read(display)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"input":true,"sequence":1}`)))
	kind, b, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, kind)
	require.JSONEq(t, `{"input":true,"sequence":1}`, string(b))
	send(key)
	read(key)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"input":false,"sequence":2}`)))
	// The upstream release and the local acknowledgment can arrive in either order.
	for i := 0; i < 2; i++ {
		kind, b, err = c.Read(ctx)
		require.NoError(t, err)
		if kind == websocket.MessageText {
			require.JSONEq(t, `{"input":false,"sequence":2}`, string(b))
		} else {
			require.Equal(t, []byte{4, 0, 0, 0, 0, 0, 0, 65}, b)
		}
	}
	send(append(bytes.Clone(key), display...))
	read(display)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte(`{"input":true,"sequence":3}`)))
	kind, b, err = c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, kind)
	require.JSONEq(t, `{"input":true,"sequence":3}`, string(b))
	send(key)
	read(key)
	require.NoError(t, svc.SetEnabled(ctx, project.ID, false))
	_, _, err = c.Read(ctx)
	require.Error(t, err)
	require.NoError(t, ctx.Err())
}
