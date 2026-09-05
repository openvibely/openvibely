package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
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
	// The list fragment must not embed the full body; it is lazily loaded per alert.
	require.NotContains(t, projectListRec.Body.String(), alert.Body)
	require.Contains(t, projectListRec.Body.String(), "/alerts/"+alert.ID+"/details")
	require.Contains(t, projectListRec.Body.String(), ">Approve<")
	require.Contains(t, projectListRec.Body.String(), ">Reject<")

	// The lazy detail fragment returns the full body scoped to the current project.
	detailReq := httptest.NewRequest(http.MethodGet, "/alerts/"+alert.ID+"/details?project_id="+project.ID, nil)
	detailRec := httptest.NewRecorder()
	detailCtx := e.NewContext(detailReq, detailRec)
	detailCtx.SetParamNames("id")
	detailCtx.SetParamValues(alert.ID)
	require.NoError(t, h.GetAlertDetail(detailCtx))
	require.Equal(t, http.StatusOK, detailRec.Code)
	require.Contains(t, detailRec.Body.String(), alert.Body)

	// Project isolation: a foreign project must not be able to load the detail.
	foreignDetailReq := httptest.NewRequest(http.MethodGet, "/alerts/"+alert.ID+"/details?project_id="+foreign.ID, nil)
	foreignDetailRec := httptest.NewRecorder()
	foreignDetailCtx := e.NewContext(foreignDetailReq, foreignDetailRec)
	foreignDetailCtx.SetParamNames("id")
	foreignDetailCtx.SetParamValues(alert.ID)
	require.Error(t, h.GetAlertDetail(foreignDetailCtx))

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

func TestHandler_AlertMutationHTMXRefreshContract(t *testing.T) {
	performHTMXMutation := func(t *testing.T, e *echo.Echo, method, target, alertID string, invoke func(echo.Context) error) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		if alertID != "" {
			c.SetParamNames("id")
			c.SetParamValues(alertID)
		}
		require.NoError(t, invoke(c))
		assertCode(t, rec, http.StatusOK)
		assertAlertUpdate(t, rec)
		require.Contains(t, rec.Body.String(), `id="alerts-content"`)
		return rec
	}

	t.Run("decision mutations refresh actionable notification controls", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			path  string
			want  models.AlertDecisionState
			apply func(echo.Context) error
		}{
			{name: "approve", path: "approve", want: models.AlertDecisionApproved},
			{name: "reject", path: "reject", want: models.AlertDecisionRejected},
			{name: "dismiss", path: "dismiss", want: models.AlertDecisionDismissed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h, e, _ := setupTestHandler(t)
				project := createProject(t, h, "Decision Refresh Project")
				alert := &models.Alert{
					ProjectID:       project.ID,
					Scope:           models.AlertScopeProject,
					Type:            models.AlertType("suggestion"),
					Severity:        models.SeverityInfo,
					Title:           "Review " + tc.name,
					Message:         "Needs a decision",
					Source:          "test",
					DecisionState:   models.AlertDecisionPending,
					ProcessingState: models.AlertProcessingUnclaimed,
				}
				require.NoError(t, h.alertSvc.Create(context.Background(), alert))
				switch tc.want {
				case models.AlertDecisionApproved:
					tc.apply = h.ApproveAlert
				case models.AlertDecisionRejected:
					tc.apply = h.RejectAlert
				case models.AlertDecisionDismissed:
					tc.apply = h.DismissAlert
				}

				rec := performHTMXMutation(t, e, http.MethodPost, "/alerts/"+alert.ID+"/"+tc.path+"?project_id="+project.ID, alert.ID, tc.apply)
				assertContains(t, rec, alert.Title)
				assertContains(t, rec, string(tc.want))
				assertNotContains(t, rec, ">Approve<")
				assertNotContains(t, rec, ">Reject<")

				stored, err := h.alertSvc.GetByID(context.Background(), project.ID, alert.ID)
				require.NoError(t, err)
				require.Equal(t, tc.want, stored.DecisionState)
			})
		}
	})

	t.Run("mark single notification read refreshes visible read row", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Read Refresh Project")
		alert := createAlert(t, h, project.ID, "Read remains visible")

		rec := performHTMXMutation(t, e, http.MethodPost, "/alerts/"+alert.ID+"/read?project_id="+project.ID, alert.ID, h.MarkAlertRead)
		assertContains(t, rec, alert.Title)
		assertNotContains(t, rec, `title="Mark as read"`)
		stored, err := h.alertSvc.GetByIDAdmin(context.Background(), alert.ID)
		require.NoError(t, err)
		require.True(t, stored.IsRead)
	})

	t.Run("mark all notifications read refreshes zero unread count", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Read All Refresh Project")
		alert1 := createAlert(t, h, project.ID, "Read all one")
		alert2 := createAlert(t, h, project.ID, "Read all two")

		rec := performHTMXMutation(t, e, http.MethodPost, "/alerts/read-all?project_id="+project.ID, "", h.MarkAllAlertsRead)
		assertContains(t, rec, alert1.Title)
		assertContains(t, rec, alert2.Title)
		assertNotContains(t, rec, `badge-error">0 unread`)
		count, err := h.alertSvc.CountUnread(context.Background(), project.ID)
		require.NoError(t, err)
		require.Zero(t, count)
	})

	t.Run("delete one notification over htmx refreshes remaining list", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Delete Refresh Project")
		deleted := createAlert(t, h, project.ID, "Delete me")
		remaining := createAlert(t, h, project.ID, "Keep me")

		rec := performHTMXMutation(t, e, http.MethodDelete, "/alerts/"+deleted.ID+"?project_id="+project.ID, deleted.ID, h.DeleteAlert)
		assertNotContains(t, rec, deleted.Title)
		assertContains(t, rec, remaining.Title)
	})

	t.Run("delete last notification over htmx refreshes empty state", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Delete Last Refresh Project")
		alert := createAlert(t, h, project.ID, "Last alert")

		rec := performHTMXMutation(t, e, http.MethodDelete, "/alerts/"+alert.ID+"?project_id="+project.ID, alert.ID, h.DeleteAlert)
		assertNotContains(t, rec, alert.Title)
		assertContains(t, rec, "No alerts. You're all clear!")
	})

	t.Run("delete all notifications refreshes empty state", func(t *testing.T) {
		h, e, _ := setupTestHandler(t)
		project := createProject(t, h, "Delete All Refresh Project")
		createAlert(t, h, project.ID, "Delete all one")
		createAlert(t, h, project.ID, "Delete all two")

		rec := performHTMXMutation(t, e, http.MethodDelete, "/alerts?project_id="+project.ID, "", h.DeleteAllAlerts)
		assertContains(t, rec, "No alerts. You're all clear!")
		count, err := h.alertSvc.CountUnread(context.Background(), project.ID)
		require.NoError(t, err)
		require.Zero(t, count)
	})
}

func TestHandler_IntegratedNotificationApprovalToImplementationTask(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	taskRepo := repository.NewTaskRepo(db, nil)
	project := createProject(t, h, "Integrated Notification Project")
	caller := &models.Task{ProjectID: project.ID, Title: "Scheduled scanner", Prompt: "scan approved notifications", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, taskRepo.Create(ctx, caller))
	runtime := service.BuildAlertRuntimeActionHandlers(service.AlertRuntimeOptions{ProjectID: project.ID, CallerTaskID: caller.ID, Source: "scheduled_task", AlertSvc: h.alertSvc})

	createdJSON, err := runtime["create_notification"](ctx, json.RawMessage(`{"type":"product_suggestion","title":"Integrated suggestion","body":"Implement after approval"}`))
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

func TestHandler_ListAlertsSupportsWorkflowFiltersAndRefreshPreservation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Alert filter project")
	foreign := createProject(t, h, "Foreign alert filter project")

	createFilteredAlert := func(projectID, title string, decision models.AlertDecisionState, processing models.AlertProcessingState, message string) *models.Alert {
		t.Helper()
		alert := &models.Alert{
			ProjectID:       projectID,
			Scope:           models.AlertScopeProject,
			Type:            models.AlertCustom,
			Severity:        models.SeverityInfo,
			Title:           title,
			Message:         message,
			Source:          "alert-filter-test",
			DecisionState:   decision,
			ProcessingState: processing,
		}
		require.NoError(t, h.alertSvc.Create(context.Background(), alert))
		return alert
	}

	pendingUnclaimed := createFilteredAlert(project.ID, "Pending unclaimed match", models.AlertDecisionPending, models.AlertProcessingUnclaimed, "needle")
	pendingFailedFirst := createFilteredAlert(project.ID, "Pending failed first", models.AlertDecisionPending, models.AlertProcessingFailed, "needle")
	pendingFailedSecond := createFilteredAlert(project.ID, "Pending failed second", models.AlertDecisionPending, models.AlertProcessingFailed, "needle")
	approvedFailed := createFilteredAlert(project.ID, "Approved failed excluded", models.AlertDecisionApproved, models.AlertProcessingFailed, "needle")
	createFilteredAlert(project.ID, "Operational excluded", models.AlertDecisionNotRequired, models.AlertProcessingNotApplicable, "other")
	foreignAlert := createFilteredAlert(foreign.ID, "Foreign pending match", models.AlertDecisionPending, models.AlertProcessingFailed, "needle")

	t.Run("combined filters and search are project scoped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID+"&decision_state=pending&processing_state=failed&search=needle", nil)
		rec := httptest.NewRecorder()
		require.NoError(t, h.ListAlerts(e.NewContext(req, rec)))
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		for _, want := range []string{pendingFailedFirst.Title, pendingFailedSecond.Title, `data-card-pagination-preserve-params="read,severity,decision_state,processing_state,type,source,sort,search"`, `value="pending" selected`, `value="needle"`, `data-card-search-initial="needle"`, `type="hidden" name="processing_state" value="failed"`, "5 unread"} {
			require.Contains(t, body, want)
		}
		for _, removed := range []string{`aria-label="Filter by processing state"`, `All processing states`} {
			require.NotContains(t, body, removed)
		}
		for _, excluded := range []string{pendingUnclaimed.Title, approvedFailed.Title, foreignAlert.Title, "Operational excluded"} {
			require.NotContains(t, body, excluded)
		}
		require.Contains(t, body, "/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id="+project.ID+"&amp;search=needle")
		require.Contains(t, body, `data-card-pagination-url="/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id=`+project.ID+`&amp;search=needle"`)
		require.Contains(t, body, "/alerts/"+pendingFailedFirst.ID+"/approve?decision_state=pending&amp;processing_state=failed&amp;project_id="+project.ID+"&amp;search=needle")
	})

	t.Run("filtered page keeps both predicates and page boundary", func(t *testing.T) {
		firstReq := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID+"&decision_state=pending&processing_state=failed&search=needle&page=0&page_size=1&card_page=1", nil)
		firstRec := httptest.NewRecorder()
		require.NoError(t, h.ListAlerts(e.NewContext(firstReq, firstRec)))
		require.Equal(t, http.StatusOK, firstRec.Code)
		require.Equal(t, "true", firstRec.Header().Get(cardPageHasMoreHeader))
		firstBody := firstRec.Body.String()
		secondReq := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID+"&decision_state=pending&processing_state=failed&search=needle&page=1&page_size=1&card_page=1", nil)
		secondRec := httptest.NewRecorder()
		require.NoError(t, h.ListAlerts(e.NewContext(secondReq, secondRec)))
		require.Equal(t, http.StatusOK, secondRec.Code)
		secondBody := secondRec.Body.String()
		require.NotEqual(t, strings.Contains(firstBody, pendingFailedFirst.Title), strings.Contains(secondBody, pendingFailedFirst.Title))
		require.NotEqual(t, strings.Contains(firstBody, pendingFailedSecond.Title), strings.Contains(secondBody, pendingFailedSecond.Title))
		require.Contains(t, secondBody, `data-card-pagination-url="/alerts?decision_state=pending&amp;processing_state=failed&amp;project_id=`+project.ID+`&amp;search=needle"`)
	})

	t.Run("mutation refresh keeps active filters", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/alerts/"+pendingUnclaimed.ID+"/approve?project_id="+project.ID+"&decision_state=pending&processing_state=unclaimed&search=needle", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetParamNames("id")
		ctx.SetParamValues(pendingUnclaimed.ID)
		require.NoError(t, h.ApproveAlert(ctx))
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		require.NotContains(t, body, pendingUnclaimed.Title)
		require.Contains(t, body, `value="pending" selected`)
		require.Contains(t, body, `value="needle"`)
		require.Contains(t, body, `data-card-search-initial="needle"`)
		require.Contains(t, body, `type="hidden" name="processing_state" value="unclaimed"`)
		require.NotContains(t, body, `aria-label="Filter by processing state"`)
		require.Contains(t, body, "/alerts?decision_state=pending&amp;processing_state=unclaimed&amp;project_id="+project.ID+"&amp;search=needle")
	})

	t.Run("search-only empty results use the filtered empty state", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID+"&search=does-not-exist", nil)
		rec := httptest.NewRecorder()
		require.NoError(t, h.ListAlerts(e.NewContext(req, rec)))
		body := rec.Body.String()
		require.Contains(t, body, "No alerts match the selected filters.")
		require.NotContains(t, body, "No alerts. You're all clear!")
		require.Contains(t, body, `value="does-not-exist"`)
		require.Contains(t, body, `data-card-search-initial="does-not-exist"`)
		require.Contains(t, body, `/alerts?project_id=`+project.ID+`&amp;search=does-not-exist`)
	})

	t.Run("invalid values fall back to unfiltered project list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID+"&decision_state=unknown&processing_state=unknown", nil)
		rec := httptest.NewRecorder()
		require.NoError(t, h.ListAlerts(e.NewContext(req, rec)))
		body := rec.Body.String()
		for _, want := range []string{pendingFailedFirst.Title, pendingFailedSecond.Title, approvedFailed.Title, "Operational excluded"} {
			require.Contains(t, body, want)
		}
		require.NotContains(t, body, foreignAlert.Title)
		require.Contains(t, body, `name="decision_state"`)
		require.Contains(t, body, `<option value="">All</option>`)
		require.NotContains(t, body, `data-card-filter-chip="decision_state"`)
		require.NotContains(t, body, `data-card-filter-chip="processing_state"`)
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

func TestHandler_MarkAlertsReadBulkPreflightsProject(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Bulk read alerts")
	foreignProject := createProject(t, h, "Foreign bulk read alerts")
	own := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "own"}
	foreign := &models.Alert{ProjectID: foreignProject.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "foreign"}
	require.NoError(t, h.alertSvc.Create(context.Background(), own))
	require.NoError(t, h.alertSvc.Create(context.Background(), foreign))

	request := func(ids ...string) *httptest.ResponseRecorder {
		payload, err := json.Marshal(bulkIDsRequest{IDs: ids})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/alerts/read-bulk?project_id="+project.ID, strings.NewReader(string(payload)))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	rec := request(own.ID, foreign.ID)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	unchanged, err := h.alertSvc.GetByIDAdmin(context.Background(), own.ID)
	require.NoError(t, err)
	require.False(t, unchanged.IsRead)

	rec = request(own.ID, own.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"updated":1}`, rec.Body.String())
	updated, err := h.alertSvc.GetByIDAdmin(context.Background(), own.ID)
	require.NoError(t, err)
	require.True(t, updated.IsRead)
}

func TestHandler_DeleteAlertsBulkDeduplicatesAndPreflightsProject(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Bulk alerts")
	foreignProject := createProject(t, h, "Foreign bulk alerts")
	own := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "own"}
	foreign := &models.Alert{ProjectID: foreignProject.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "foreign"}
	require.NoError(t, h.alertSvc.Create(context.Background(), own))
	require.NoError(t, h.alertSvc.Create(context.Background(), foreign))

	body := strings.NewReader(fmt.Sprintf(`{"ids":[%q,%q]}`, own.ID, foreign.ID))
	req := httptest.NewRequest(http.MethodDelete, "/alerts/bulk?project_id="+project.ID, body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	remaining, err := h.alertSvc.ListSummariesPage(context.Background(), project.ID, models.AlertListFilter{Limit: 10})
	require.NoError(t, err)
	require.Len(t, remaining, 1)

	body = strings.NewReader(fmt.Sprintf(`{"ids":[%q,%q]}`, own.ID, own.ID))
	req = httptest.NewRequest(http.MethodDelete, "/alerts/bulk?project_id="+project.ID, body)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"deleted":1}`, rec.Body.String())
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
