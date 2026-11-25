package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/stretchr/testify/require"
)

func TestPersonalitiesRemoved(t *testing.T) {
	tc := NewStoreTestContext(t)
	for _, route := range tc.echo.Routes() {
		require.False(t, strings.HasPrefix(route.Path, "/personality"), "retired route: %s %s", route.Method, route.Path)
	}
	for _, path := range []string{"/personality", "/personality/custom/old"} {
		rec := tc.HTTP().Get(path).Execute()
		require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	}
	for _, name := range []string{"list_personalities", "get_personality", "set_personality", "save_custom_personality"} {
		require.Nil(t, chatcontrol.Get(name), "retired tool: %s", name)
	}
	_, exists := serverActions["personality.operation"]
	require.False(t, exists)
}
