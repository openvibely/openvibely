package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestHandler_ApproveAlertIsProjectScoped(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Approval Project")
	foreign := createProject(t, h, "Foreign Approval Project")
	a := &models.Alert{ProjectID: project.ID, Scope: models.AlertScopeProject, Type: models.AlertType("suggestion"), Severity: models.SeverityInfo,
		Title: "Review suggestion", Body: "Inspect me", Source: "test", DecisionState: models.AlertDecisionPending, ProcessingState: models.AlertProcessingUnclaimed}
	require.NoError(t, h.alertSvc.Create(context.Background(), a))

	foreignReq := httptest.NewRequest(http.MethodPost, "/alerts/"+a.ID+"/approve?project_id="+foreign.ID, nil)
	foreignRec := httptest.NewRecorder()
	foreignCtx := e.NewContext(foreignReq, foreignRec)
	foreignCtx.SetParamNames("id")
	foreignCtx.SetParamValues(a.ID)
	err := h.ApproveAlert(foreignCtx)
	require.Error(t, err)
	unchanged, getErr := h.alertSvc.GetByID(context.Background(), project.ID, a.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.AlertDecisionPending, unchanged.DecisionState)

	req := httptest.NewRequest(http.MethodPost, "/alerts/"+a.ID+"/approve?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(a.ID)
	require.NoError(t, h.ApproveAlert(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "approved")
	require.NotContains(t, rec.Body.String(), ">Approve<")
	approved, getErr := h.alertSvc.GetByID(context.Background(), project.ID, a.ID)
	require.NoError(t, getErr)
	require.Equal(t, models.AlertDecisionApproved, approved.DecisionState)
	require.Equal(t, models.AlertProcessingUnclaimed, approved.ProcessingState)
}

func TestHandler_RejectAlertAndActionableVisibilityAreProjectScoped(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Review Project")
	foreign := createProject(t, h, "Foreign Review Project")
	alert := &models.Alert{ProjectID: project.ID, Scope: models.AlertScopeProject, Type: "suggestion", Severity: models.SeverityInfo,
		Title: "Reject this suggestion", Body: "Project-only review body", Source: "test", DecisionState: models.AlertDecisionPending, ProcessingState: models.AlertProcessingUnclaimed}
	require.NoError(t, h.alertSvc.Create(context.Background(), alert))

	foreignListReq := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+foreign.ID, nil)
	foreignListRec := httptest.NewRecorder()
	require.NoError(t, h.ListAlerts(e.NewContext(foreignListReq, foreignListRec)))
	require.NotContains(t, foreignListRec.Body.String(), alert.Title)
	require.NotContains(t, foreignListRec.Body.String(), alert.Body)

	projectListReq := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	projectListRec := httptest.NewRecorder()
	require.NoError(t, h.ListAlerts(e.NewContext(projectListReq, projectListRec)))
	require.Contains(t, projectListRec.Body.String(), alert.Title)
	require.Contains(t, projectListRec.Body.String(), alert.Body)
	require.Contains(t, projectListRec.Body.String(), ">Approve<")
	require.Contains(t, projectListRec.Body.String(), ">Reject<")

	foreignRejectReq := httptest.NewRequest(http.MethodPost, "/alerts/"+alert.ID+"/reject?project_id="+foreign.ID, nil)
	foreignRejectRec := httptest.NewRecorder()
	foreignRejectCtx := e.NewContext(foreignRejectReq, foreignRejectRec)
	foreignRejectCtx.SetParamNames("id")
	foreignRejectCtx.SetParamValues(alert.ID)
	require.Error(t, h.RejectAlert(foreignRejectCtx))

	rejectReq := httptest.NewRequest(http.MethodPost, "/alerts/"+alert.ID+"/reject?project_id="+project.ID, nil)
	rejectReq.Header.Set("HX-Request", "true")
	rejectRec := httptest.NewRecorder()
	rejectCtx := e.NewContext(rejectReq, rejectRec)
	rejectCtx.SetParamNames("id")
	rejectCtx.SetParamValues(alert.ID)
	require.NoError(t, h.RejectAlert(rejectCtx))
	require.Contains(t, rejectRec.Body.String(), "rejected")
	require.NotContains(t, rejectRec.Body.String(), ">Approve<")
	stored, err := h.alertSvc.GetByID(context.Background(), project.ID, alert.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertDecisionRejected, stored.DecisionState)
}

func TestHandler_IntegratedNotificationApprovalToImplementationTask(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	project := createProject(t, h, "Integrated Notification Project")
	caller := &models.Task{ProjectID: project.ID, Title: "Scheduled scanner", Prompt: "scan approved notifications", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	runtime := service.BuildAlertRuntimeActionHandlers(service.AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: h.alertSvc})

	createdJSON, err := runtime["create_notification"](ctx, json.RawMessage(`{"type":"product_suggestion","title":"Integrated suggestion","body":"Implement after approval","idempotency_key":"integrated-flow"}`))
	require.NoError(t, err)
	var created struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))

	listReq := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	listRec := httptest.NewRecorder()
	require.NoError(t, h.ListAlerts(e.NewContext(listReq, listRec)))
	require.Contains(t, listRec.Body.String(), "Integrated suggestion")
	require.Contains(t, listRec.Body.String(), "pending")

	approveReq := httptest.NewRequest(http.MethodPost, "/alerts/"+created.Notification.ID+"/approve?project_id="+project.ID, nil)
	approveReq.Header.Set("HX-Request", "true")
	approveRec := httptest.NewRecorder()
	approveCtx := e.NewContext(approveReq, approveRec)
	approveCtx.SetParamNames("id")
	approveCtx.SetParamValues(created.Notification.ID)
	require.NoError(t, h.ApproveAlert(approveCtx))

	approvedJSON, err := runtime["list_alerts"](ctx, json.RawMessage(`{"decision_state":"approved","processing_state":"unclaimed"}`))
	require.NoError(t, err)
	require.Contains(t, approvedJSON, created.Notification.ID)
	_, err = runtime["get_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`"}`))
	require.NoError(t, err)
	_, err = runtime["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`"}`))
	require.NoError(t, err)
	linkedJSON, err := runtime["create_alert_implementation_task"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`","title":"Implement integrated suggestion","prompt":"Implement the approved suggestion.","priority":2}`))
	require.NoError(t, err)
	var linked struct {
		ImplementationTaskID string `json:"implementation_task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(linkedJSON), &linked))
	require.NotEmpty(t, linked.ImplementationTaskID)
	stored, err := h.alertSvc.GetByID(ctx, project.ID, created.Notification.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertProcessingImplementationTaskLinked, stored.ProcessingState)
	require.Equal(t, linked.ImplementationTaskID, *stored.ImplementationTaskID)
	implementation, err := taskRepo.GetByID(ctx, linked.ImplementationTaskID)
	require.NoError(t, err)
	require.Equal(t, project.ID, implementation.ProjectID)
	require.Equal(t, models.CategoryBacklog, implementation.Category)
}

func TestHandler_RuntimeClaimAlertRejectsInvalidLeaseWithoutMutation(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	project := createProject(t, h, "Runtime Claim Lease Integration Project")
	caller := &models.Task{ProjectID: project.ID, Title: "Scheduled scanner", Prompt: "scan approved notifications", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	runtime := service.BuildAlertRuntimeActionHandlers(service.AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: h.alertSvc})

	createdJSON, err := runtime["create_notification"](ctx, json.RawMessage(`{"type":"product_suggestion","title":"Invalid lease integration","body":"Review invalid lease behavior"}`))
	require.NoError(t, err)
	var created struct {
		Notification models.Alert `json:"notification"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))

	approveReq := httptest.NewRequest(http.MethodPost, "/alerts/"+created.Notification.ID+"/approve?project_id="+project.ID, nil)
	approveReq.Header.Set("HX-Request", "true")
	approveRec := httptest.NewRecorder()
	approveCtx := e.NewContext(approveReq, approveRec)
	approveCtx.SetParamNames("id")
	approveCtx.SetParamValues(created.Notification.ID)
	require.NoError(t, h.ApproveAlert(approveCtx))

	_, err = runtime["claim_alert"](ctx, json.RawMessage(`{"alert_id":"`+created.Notification.ID+`","lease_seconds":90000}`))
	require.ErrorContains(t, err, "lease_seconds must be between 1 and 86400")

	stored, err := h.alertSvc.GetByID(ctx, project.ID, created.Notification.ID)
	require.NoError(t, err)
	require.Equal(t, models.AlertProcessingUnclaimed, stored.ProcessingState)
	require.Empty(t, stored.Claimant)
	require.Nil(t, stored.ClaimedAt)
	require.Nil(t, stored.ClaimExpiresAt)
}

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
		updatedAlert, err := h.alertSvc.GetByIDAdmin(context.Background(), alert.ID)
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
			alert, err := h.alertSvc.GetByIDAdmin(ctx, alertID)
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
		updatedAlert1, err := h.alertSvc.GetByIDAdmin(ctx, alert1.ID)
		require.NoError(t, err)
		require.True(t, updatedAlert1.IsRead)

		updatedAlert2, err := h.alertSvc.GetByIDAdmin(ctx, alert2.ID)
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
		req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alert.ID+"?project_id="+project.ID, nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(alert.ID)

		err := h.DeleteAlert(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, rec.Code)

		// Verify alert is deleted
		_, err = h.alertSvc.GetByIDAdmin(context.Background(), alert.ID)
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

		req := httptest.NewRequest(http.MethodDelete, "/alerts/"+alert.ID+"?project_id="+project.ID, nil)
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
			_, err := h.alertSvc.GetByIDAdmin(ctx, alertID)
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
		_, err = h.alertSvc.GetByIDAdmin(ctx, alert1.ID)
		require.Error(t, err)

		alert2Check, err := h.alertSvc.GetByIDAdmin(ctx, alert2.ID)
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
