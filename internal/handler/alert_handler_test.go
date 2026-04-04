package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandler_ListAlerts(t *testing.T) {
	t.Run("lists alerts for current project", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create some alerts
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		alert2 := createAlert(t, h, project.ID, "Alert 2")
		alert3 := createAlert(t, h, project.ID, "Alert 3")

		// List alerts
		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.ListAlerts(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, alert1.Title)
		assertContains(t, rec, alert2.Title)
		assertContains(t, rec, alert3.Title)
		assertContains(t, rec, "unread")
	})

	t.Run("lists alerts with HTMX", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create some alerts
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		alert2 := createAlert(t, h, project.ID, "Alert 2")

		// List alerts with HTMX
		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.ListAlerts(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, alert1.Title)
		assertContains(t, rec, alert2.Title)
		// HTMX response should not contain full layout
		assertNotContains(t, rec, "<html")
		assertNotContains(t, rec, "<!DOCTYPE")
	})

	t.Run("shows unread count", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create alerts (all are unread by default)
		createAlert(t, h, project.ID, "Unread 1")
		createAlert(t, h, project.ID, "Unread 2")
		createAlert(t, h, project.ID, "Unread 3")

		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.ListAlerts(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "3 unread")
	})
}

func TestHandler_MarkAlertRead(t *testing.T) {
	t.Run("marks single alert as read", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")
		alert := createAlert(t, h, project.ID, "Unread Alert")

		// Mark as read
		req := httptest.NewRequest(http.MethodPatch, "/alerts/"+alert.ID+"/read?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert.ID)

		err := h.MarkAlertRead(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertAlertUpdate(t, rec)

		// Verify alert is marked as read
		updatedAlert, err := h.alertSvc.GetByID(context.Background(), alert.ID)
		require.NoError(t, err)
		require.True(t, updatedAlert.IsRead)
	})

	t.Run("updates unread count after marking read", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create multiple alerts
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		createAlert(t, h, project.ID, "Alert 2")
		createAlert(t, h, project.ID, "Alert 3")

		// Mark first alert as read
		req := httptest.NewRequest(http.MethodPatch, "/alerts/"+alert1.ID+"/read?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert1.ID)

		err := h.MarkAlertRead(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "2 unread")
	})

	t.Run("triggers alert update", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")
		alert := createAlert(t, h, project.ID, "Alert")

		req := httptest.NewRequest(http.MethodPatch, "/alerts/"+alert.ID+"/read?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert.ID)

		err := h.MarkAlertRead(c)
		require.NoError(t, err)
		assertAlertUpdate(t, rec)
	})
}

func TestHandler_MarkAllAlertsRead(t *testing.T) {
	t.Run("marks all alerts as read for project", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create multiple alerts
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		alert2 := createAlert(t, h, project.ID, "Alert 2")
		alert3 := createAlert(t, h, project.ID, "Alert 3")

		// Mark all as read
		req := httptest.NewRequest(http.MethodPost, "/alerts/read-all?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.MarkAllAlertsRead(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertAlertUpdate(t, rec)
		assertNotContains(t, rec, "badge-error badge-sm ml-2")

		// Verify all alerts are marked as read
		ctx := context.Background()
		for _, alertID := range []string{alert1.ID, alert2.ID, alert3.ID} {
			alert, err := h.alertSvc.GetByID(ctx, alertID)
			require.NoError(t, err)
			require.True(t, alert.IsRead, "Alert %s should be marked as read", alertID)
		}
	})

	t.Run("only marks alerts for current project", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project1 := createProject(t, h, "Project 1")
		project2 := createProject(t, h, "Project 2")

		// Create alerts in different projects
		alert1 := createAlert(t, h, project1.ID, "Project 1 Alert")
		alert2 := createAlert(t, h, project2.ID, "Project 2 Alert")

		// Mark all as read for project 1
		req := httptest.NewRequest(http.MethodPost, "/alerts/read-all?project_id="+project1.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.MarkAllAlertsRead(c)
		require.NoError(t, err)

		// Verify only project 1 alert is marked as read
		ctx := context.Background()
		updatedAlert1, err := h.alertSvc.GetByID(ctx, alert1.ID)
		require.NoError(t, err)
		require.True(t, updatedAlert1.IsRead)

		updatedAlert2, err := h.alertSvc.GetByID(ctx, alert2.ID)
		require.NoError(t, err)
		require.False(t, updatedAlert2.IsRead)
	})
}

func TestHandler_DeleteAlert(t *testing.T) {
	t.Run("deletes single alert", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")
		alert := createAlert(t, h, project.ID, "To Delete")

		// Delete alert
		req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alert.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert.ID)

		err := h.DeleteAlert(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		// Verify alert is deleted
		_, err = h.alertSvc.GetByID(context.Background(), alert.ID)
		require.Error(t, err)
	})

	t.Run("deletes with HTMX and updates list", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		alert2 := createAlert(t, h, project.ID, "Alert 2")

		// Delete first alert with HTMX
		req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alert1.ID+"?project_id="+project.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert1.ID)

		err := h.DeleteAlert(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertAlertUpdate(t, rec)

		// Response should contain updated list without deleted alert
		assertNotContains(t, rec, alert1.Title)
		assertContains(t, rec, alert2.Title)
	})

	t.Run("triggers alert update", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")
		alert := createAlert(t, h, project.ID, "Alert")

		req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alert.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert.ID)

		err := h.DeleteAlert(c)
		require.NoError(t, err)
		assertAlertUpdate(t, rec)
	})
}

func TestHandler_GetUnreadAlertCount(t *testing.T) {
	t.Run("returns correct unread count", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create alerts
		createAlert(t, h, project.ID, "Unread 1")
		createAlert(t, h, project.ID, "Unread 2")
		createAlert(t, h, project.ID, "Unread 3")

		// Get unread count
		req := httptest.NewRequest(http.MethodGet, "/alerts/unread-count?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.GetUnreadAlertCount(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertContains(t, rec, "3")
		assertContains(t, rec, "badge-error")
	})

	t.Run("returns zero for no alerts", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		req := httptest.NewRequest(http.MethodGet, "/alerts/unread-count?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.GetUnreadAlertCount(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		// Zero count typically shows no badge or a "0"
		body := rec.Body.String()
		require.True(t, contains(body, "0") || !contains(body, "alert-badge"))
	})
}

func TestHandler_DeleteAllAlerts(t *testing.T) {
	t.Run("deletes all alerts for project", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create multiple alerts
		alert1 := createAlert(t, h, project.ID, "Alert 1")
		alert2 := createAlert(t, h, project.ID, "Alert 2")
		alert3 := createAlert(t, h, project.ID, "Alert 3")

		// Delete all
		req := httptest.NewRequest(http.MethodPost, "/alerts/delete-all?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.DeleteAllAlerts(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)
		assertAlertUpdate(t, rec)
		assertContains(t, rec, "No alerts. You're all clear!")

		// Verify all alerts are deleted
		ctx := context.Background()
		for _, alertID := range []string{alert1.ID, alert2.ID, alert3.ID} {
			_, err := h.alertSvc.GetByID(ctx, alertID)
			require.Error(t, err, "Alert %s should be deleted", alertID)
		}
	})

	t.Run("only deletes alerts for current project", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project1 := createProject(t, h, "Project 1")
		project2 := createProject(t, h, "Project 2")

		// Create alerts in different projects
		alert1 := createAlert(t, h, project1.ID, "Project 1 Alert")
		alert2 := createAlert(t, h, project2.ID, "Project 2 Alert")

		// Delete all for project 1
		req := httptest.NewRequest(http.MethodPost, "/alerts/delete-all?project_id="+project1.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.DeleteAllAlerts(c)
		require.NoError(t, err)

		// Verify only project 1 alert is deleted
		ctx := context.Background()
		_, err = h.alertSvc.GetByID(ctx, alert1.ID)
		require.Error(t, err)

		alert2Check, err := h.alertSvc.GetByID(ctx, alert2.ID)
		require.NoError(t, err)
		require.Equal(t, alert2.ID, alert2Check.ID)
	})

	t.Run("returns empty list after deletion", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Test Project")

		// Create and delete alerts
		createAlert(t, h, project.ID, "Alert 1")
		createAlert(t, h, project.ID, "Alert 2")

		req := httptest.NewRequest(http.MethodPost, "/alerts/delete-all?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := h.DeleteAllAlerts(c)
		require.NoError(t, err)
		assertCode(t, rec, http.StatusOK)

		// Should return empty alerts content
		assertNotContains(t, rec, "Alert 1")
		assertNotContains(t, rec, "Alert 2")
		assertContains(t, rec, "No alerts. You're all clear!")
	})
}

// Helper function to check if string contains substring
