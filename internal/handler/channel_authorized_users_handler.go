package handler

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

type authorizedUserCRUD[T any] struct {
	list   func(context.Context, string) ([]T, error)
	render func([]T, string) templ.Component
}

func (crud authorizedUserCRUD[T]) listUsers(c echo.Context, projectID string) error {
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	return crud.reload(c, projectID)
}

func (crud authorizedUserCRUD[T]) createUser(c echo.Context, projectID string, create func(context.Context) error) error {
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	if err := create(c.Request().Context()); err != nil {
		if httpErr, ok := err.(*echo.HTTPError); ok {
			return httpErr
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to add authorized user: "+err.Error())
	}
	return crud.reload(c, projectID)
}

func (crud authorizedUserCRUD[T]) deleteUser(
	c echo.Context,
	id string,
	projectID string,
	loadProjectID func(context.Context, string) (string, bool, error),
	deleteUser func(context.Context, string) error,
) error {
	fallbackProjectID, found, err := loadProjectID(c.Request().Context(), id)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user")
	}
	if !found {
		return echo.NewHTTPError(http.StatusNotFound, "User not found")
	}
	if projectID == "" {
		projectID = fallbackProjectID
	}
	if err := deleteUser(c.Request().Context(), id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to remove user: "+err.Error())
	}
	return crud.reload(c, projectID)
}

func (crud authorizedUserCRUD[T]) reload(c echo.Context, projectID string) error {
	users, err := crud.list(c.Request().Context(), projectID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load authorized users")
	}
	return render(c, http.StatusOK, crud.render(users, projectID))
}
