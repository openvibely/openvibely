package handler

import (
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
	Page       int
	PageSize   int
	Offset     int
	Search     string
	IsFragment bool
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
