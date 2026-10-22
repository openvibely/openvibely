package handler

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

type nativeLiveTicket struct {
	cookie  string
	expires time.Time
}
type nativeLiveTickets struct {
	sync.Mutex
	entries map[string]nativeLiveTicket
}

const nativeLiveContextKey = "native-live-ticket"

func nativeLiveOrigin(origin string) bool {
	switch origin {
	case "null", "wails://localhost", "http://wails.localhost", "https://wails.localhost":
		return true
	}
	return false
}

// Wails' custom asset protocol cannot upgrade HTTP. Mint a single-use capability
// through its authenticated HTTP proxy, then connect directly to loopback. The
// capability carries the original session cookie server-side, never in JS.
func (h *Handler) NativeLiveTicket(c echo.Context) error {
	r := c.Request()
	if !h.desktopMode || r.Header.Get("X-OpenVibely-Native-Window") != "1" || r.Header.Get("X-OpenVibely-Live") != "1" || (r.Header.Get("Origin") != "" && !nativeLiveOrigin(r.Header.Get("Origin"))) {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	address, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(bytes[:])
	h.nativeLiveTickets.Lock()
	defer h.nativeLiveTickets.Unlock()
	if h.nativeLiveTickets.entries == nil {
		h.nativeLiveTickets.entries = make(map[string]nativeLiveTicket)
	}
	now := time.Now()
	for key, ticket := range h.nativeLiveTickets.entries {
		if !now.Before(ticket.expires) {
			delete(h.nativeLiveTickets.entries, key)
		}
	}
	if len(h.nativeLiveTickets.entries) >= 256 {
		return echo.NewHTTPError(http.StatusTooManyRequests)
	}
	h.nativeLiveTickets.entries[token] = nativeLiveTicket{cookie: r.Header.Get("Cookie"), expires: now.Add(time.Minute)}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]string{"url": "ws://" + address.String() + "/events/ws", "ticket": token})
}

func (h *Handler) consumeNativeLiveTicket(c echo.Context) error {
	if c.Request().URL.Path != "/events/ws" || c.Get(nativeLiveContextKey) != nil {
		return nil
	}
	var token string
	for _, protocol := range strings.Split(c.Request().Header.Get("Sec-WebSocket-Protocol"), ",") {
		protocol = strings.TrimSpace(protocol)
		if strings.HasPrefix(protocol, "ticket.") {
			token = strings.TrimPrefix(protocol, "ticket.")
			break
		}
	}
	if token == "" {
		return nil
	}
	if !h.desktopMode || !nativeLiveOrigin(c.Request().Header.Get("Origin")) {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	h.nativeLiveTickets.Lock()
	ticket, ok := h.nativeLiveTickets.entries[token]
	delete(h.nativeLiveTickets.entries, token)
	h.nativeLiveTickets.Unlock()
	if !ok || !time.Now().Before(ticket.expires) {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	r := c.Request().Clone(c.Request().Context())
	r.Header.Set("Cookie", ticket.cookie)
	c.SetRequest(r)
	c.Set(nativeLiveContextKey, true)
	return nil
}
