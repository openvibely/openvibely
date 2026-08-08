package handler

import (
	"context"
	"net/http"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
)

type authorizedUserCRUD[T any] struct {
	list                 func(context.Context, string) ([]T, error)
	render               func([]T, string) templ.Component
	getByID              func(context.Context, string) (*T, error)
	delete               func(context.Context, string) error
	projectID            func(*T) string
	notConfiguredMessage string
	configured           bool
}

func (crud authorizedUserCRUD[T]) listHandler(c echo.Context) error {
	if err := crud.requireConfigured(); err != nil {
		return err
	}
	return crud.listUsers(c, c.QueryParam("project_id"))
}

func (crud authorizedUserCRUD[T]) createHandler(c echo.Context, create func(context.Context, string) error) error {
	if err := crud.requireConfigured(); err != nil {
		return err
	}
	projectID := c.FormValue("project_id")
	return crud.createUser(c, projectID, func(ctx context.Context) error {
		return create(ctx, projectID)
	})
}

func (crud authorizedUserCRUD[T]) deleteHandler(c echo.Context) error {
	if err := crud.requireConfigured(); err != nil {
		return err
	}
	return crud.deleteUser(
		c,
		c.Param("id"),
		c.QueryParam("project_id"),
		authorizedUserProjectLookup(crud.getByID, crud.projectID),
		crud.delete,
	)
}

func (crud authorizedUserCRUD[T]) requireConfigured() error {
	if !crud.configured {
		return echo.NewHTTPError(http.StatusInternalServerError, crud.notConfiguredMessage)
	}
	return nil
}

func authorizedUserProjectLookup[T any](
	getByID func(context.Context, string) (*T, error),
	projectID func(*T) string,
) func(context.Context, string) (string, bool, error) {
	return func(ctx context.Context, id string) (string, bool, error) {
		user, err := getByID(ctx, id)
		if err != nil || user == nil {
			return "", user != nil, err
		}
		return projectID(user), true, nil
	}
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
