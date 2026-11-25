package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

const (
	cardPageDefaultSize    = 20
	cardPageMaxSize        = 50
	cardPageRefreshMaxSize = 500
	cardPageHasMoreHeader  = "X-OpenVibely-Card-Page-Has-More"
)

type cardPageRequest struct {
	Page       int
	PageSize   int
	Offset     int
	Search     string
	IsFragment bool
}

func parseCardPageRequest(c echo.Context) cardPageRequest {
	initCardCursor(c)
	page := parseNonNegativeQueryInt(c, "page", 0)
	pageSize := parseNonNegativeQueryInt(c, "page_size", cardPageDefaultSize)
	isRefreshWindow := c.QueryParam("card_window") == "1"
	isFragment := page > 0 || c.QueryParam("card_page") == "1"
	if isRefreshWindow {
		// Mutation and live refreshes re-render the currently loaded client
		// window from offset zero so later-page anchors and focus survive.
		page = 0
		isFragment = false
	} else if !isFragment {
		// Full documents and ordinary HTMX refreshes advertise the fixed loader
		// size in their root. Custom sizes are for explicit page fragments only.
		page = 0
		pageSize = cardPageDefaultSize
	}
	if pageSize == 0 {
		pageSize = cardPageDefaultSize
	}
	maxPageSize := cardPageMaxSize
	if isRefreshWindow {
		maxPageSize = cardPageRefreshMaxSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	maxInt := int(^uint(0) >> 1)
	if page > maxInt/pageSize {
		page = maxInt / pageSize
	}
	offset := page * pageSize
	if isFragment && !isRefreshWindow {
		if explicitOffset, err := strconv.Atoi(strings.TrimSpace(c.QueryParam("offset"))); err == nil && explicitOffset >= 0 {
			offset = explicitOffset
		}
	}
	if s := cardcursor.From(c.Request().Context()); s != nil {
		s.Limit = pageSize + 1
	}
	return cardPageRequest{
		Page:       page,
		PageSize:   pageSize,
		Offset:     offset,
		Search:     strings.TrimSpace(c.QueryParam("search")),
		IsFragment: isFragment,
	}
}

func parseNonNegativeQueryInt(c echo.Context, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.QueryParam(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

const bulkDeleteMaxItems = 50

type bulkIDsRequest struct {
	IDs []string `json:"ids"`
}

func decodeBulkIDs(r io.Reader) ([]string, error) {
	decoder := json.NewDecoder(io.LimitReader(r, 64<<10))
	decoder.DisallowUnknownFields()
	var request bulkIDsRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid JSON request: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(request.IDs))
	ids := make([]string, 0, len(request.IDs))
	for _, id := range request.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("identifiers must not be empty")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("at least one identifier is required")
	}
	if len(ids) > bulkDeleteMaxItems {
		return nil, fmt.Errorf("at most %d identifiers may be deleted at once", bulkDeleteMaxItems)
	}
	return ids, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("request must contain exactly one JSON object")
	}
	return nil
}

func allowlistedQuery(c echo.Context, key, fallback string, allowed ...string) string {
	value := strings.TrimSpace(c.QueryParam(key))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func optionalBoolString(value *bool) string {
	if value == nil {
		return ""
	}
	return strconv.FormatBool(*value)
}

func optionalBoolQuery(c echo.Context, key string) *bool {
	switch strings.TrimSpace(c.QueryParam(key)) {
	case "true":
		value := true
		return &value
	case "false":
		value := false
		return &value
	default:
		return nil
	}
}

func setCardPageResponse(c echo.Context, hasMore bool) {
	c.Response().Header().Set(cardPageHasMoreHeader, strconv.FormatBool(hasMore))
	c.Response().Header().Set("X-OpenVibely-Card-Cursor", cardcursor.Encoded(c.Request().Context()))
	c.Response().Header().Set("Cache-Control", "no-store")
}

func (h *Handler) renderCardBrowserPage(c echo.Context, currentProjectID string, page cardPageRequest, hasMore bool, content templ.Component, fullPage func([]models.Project, string) templ.Component) error {
	if isHTMX(c) || page.IsFragment {
		setCardPageResponse(c, hasMore)
		return render(c, http.StatusOK, content)
	}
	projects, _ := h.projectSvc.ListSelectorOptions(c.Request().Context())
	return render(c, http.StatusOK, fullPage(projects, currentProjectID))
}

func cardPageItems[T any](items []T, pageSize int) ([]T, bool) {
	if len(items) <= pageSize {
		return items, false
	}
	return items[:pageSize], true
}

// Offset parameters remain accepted for older clients; the current loader uses
// only cursors. Non-card store APIs are unaffected.
func initCardCursor(c echo.Context) {
	if c.QueryParam("cursor") == "" && (c.QueryParam("offset") != "" || parseNonNegativeQueryInt(c, "page", 0) > 0) && c.QueryParam("card_window") != "1" {
		return
	}
	// Use the route's leading segment, never user-controlled names later in it.
	path := c.Path()
	if path == "" {
		path = c.Request().URL.Path
	}
	section, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	kind := ""
	switch section {
	case "automations", "alerts", "models", "agents", "skills":
		kind = section
	case "channels":
		kind = "webhooks"
	}
	if kind == "" {
		return
	}
	query := cardCursorListQuery(c, kind)
	principal, _ := models.PrincipalFromContext(c.Request().Context())
	order := query.Get("sort")
	encoded := c.QueryParam("cursor")
	if c.QueryParam("card_window") == "1" {
		encoded = ""
	}
	s := cardcursor.New(kind, order, kind+"?"+query.Encode()+"\n"+principal.UserID, encoded)
	c.SetRequest(c.Request().WithContext(cardcursor.With(c.Request().Context(), s)))
}

// Only normalized list inputs belong to a continuation scope. Mutation targets
// (for example skill_project_id or new_default_id) must not affect the next GET.
func cardCursorListQuery(c echo.Context, kind string) url.Values {
	query := url.Values{}
	set := func(key, value string) {
		if value != "" {
			query.Set(key, value)
		}
	}
	allowed := func(key, fallback string, values ...string) { set(key, allowlistedQuery(c, key, fallback, values...)) }
	boolean := func(key string) { set(key, optionalBoolString(optionalBoolQuery(c, key))) }
	project := strings.TrimSpace(c.QueryParam("project_id"))
	if resolved, ok := c.Get("cardProjectID").(string); ok {
		project = resolved
	}
	// Aggregate actions temporarily use the item's owner for the write, while
	// the refreshed list still belongs to the original workspace.
	set("project_id", layout.WorkspaceProjectID(c.Request().Context(), project))
	set("search", strings.TrimSpace(c.QueryParam("search")))
	switch kind {
	case "automations":
		filter := automationCardListFilter(c, cardPageRequest{})
		state := automationCardListState("", filter)
		set("sort", state.Sort)
		for key, value := range state.Filters {
			set(key, value)
		}
	case "models":
		state := modelCardListState("", modelCardListFilter(c, cardPageRequest{}))
		set("sort", state.Sort)
		for key, value := range state.Filters {
			set(key, value)
		}
	case "alerts":
		state := alertCardListState("", alertListFilter(c, cardPageRequest{}))
		set("sort", state.Sort)
		for key, value := range state.Filters {
			set(key, value)
		}
	case "agents":
		allowed("sort", "name_asc", "name_asc", "name_desc", "updated_desc", "created_desc")
		boolean("enabled")
		allowed("scope", "", "global", "project")
		allowed("origin", "", "custom", "generated", "protected")
	case "skills":
		allowed("sort", "name_asc", "name_asc", "name_desc", "scope", "source")
		for _, key := range []string{"enabled", "always_use", "archived"} {
			boolean(key)
		}
		for _, key := range []string{"scope", "source"} {
			allowed(key, "", "global", "project")
		}
	case "webhooks":
		allowed("sort", "name_asc", "name_asc", "name_desc")
		allowed("type", "", "github", "slack", "telegram", "discord", "x", "email", "webhook", "outbound_targets")
		allowed("connection_state", "", "connected", "configured", "disconnected")
		boolean("webhook_enabled")
	}
	return query
}

func finishCardPage[T any](c echo.Context, items []T, pageSize int) ([]T, bool) {
	ctx := c.Request().Context()
	if s := cardcursor.From(ctx); s != nil {
		result, more, err := cardcursor.Finish(ctx, items, pageSize)
		if err != nil {
			s.Err = err
		}
		c.Response().Header().Set("X-OpenVibely-Card-Cursor", cardcursor.Encoded(ctx))
		return result, more
	}
	return cardPageItems(items, pageSize)
}

func (h *Handler) parseCardPageRequest(c echo.Context) cardPageRequest {
	if h.projectSvc != nil {
		if project, err := h.getCurrentProjectID(c); err == nil {
			c.Set("cardProjectID", project)
		}
	}
	return parseCardPageRequest(c)
}
