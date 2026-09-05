package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
)

const (
	cardPageDefaultSize    = 20
	cardPageMaxSize        = 50
	cardPageRefreshMaxSize = 500
	cardPageHasMoreHeader  = "X-OpenVibely-Card-Page-Has-More"
)

type cardPageRequest struct {
	Page              int
	PageSize          int
	Offset            int
	Search            string
	IsFragment        bool
	PersonalityActive *bool
	PersonalityKind   string
	PersonalitySort   string
}

func parseCardPageRequest(c echo.Context) cardPageRequest {
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
	return cardPageRequest{
		Page:              page,
		PageSize:          pageSize,
		Offset:            offset,
		Search:            strings.TrimSpace(c.QueryParam("search")),
		IsFragment:        isFragment,
		PersonalityActive: optionalBoolQuery(c, "active"),
		PersonalityKind:   allowlistedQuery(c, "kind", "", "base", "built_in", "custom", "override"),
		PersonalitySort:   allowlistedQuery(c, "sort", "curated", "curated", "name_asc", "name_desc"),
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
	c.Response().Header().Set("Cache-Control", "no-store")
}

func cardPageItems[T any](items []T, pageSize int) ([]T, bool) {
	if len(items) <= pageSize {
		return items, false
	}
	return items[:pageSize], true
}
