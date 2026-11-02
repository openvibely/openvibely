package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// Exercise real chart instances against real HTTP handlers and two-project data.
func TestAllProjectsEveryAnalyticsChartInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	ctx := t.Context()
	for _, name := range []string{"First", "Second"} {
		project := createProject(t, h, name)
		config := &models.LLMConfig{Name: name + " model", Provider: models.ProviderTest, Model: name}
		require.NoError(t, stores.LLMConfigs.Create(ctx, config))
		agent := createRichTaskUIAgent(t, stores.Agents, name+" Agent")
		for sample := 0; sample < 3; sample++ {
			task := &models.Task{ProjectID: project.ID, Title: name + " task " + strconv.Itoa(sample), Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2, AgentDefinitionID: &agent.ID}
			require.NoError(t, stores.Tasks.Create(ctx, task))
			goal := &models.TaskGoal{TaskID: task.ID, GoalID: task.ID + "-goal", Objective: "Finish", Status: models.TaskGoalStatusActive}
			require.NoError(t, stores.TaskGoals.CreateOrReplace(ctx, goal))
			_, err := stores.TaskGoals.MarkAchieved(ctx, task.ID, goal.GoalID, "Done")
			require.NoError(t, err)
			for i, status := range []models.ExecutionStatus{models.ExecCompleted, models.ExecFailed, models.ExecCompleted} {
				run := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, IsFollowup: i > 0}
				require.NoError(t, stores.Executions.Create(ctx, run))
				started := time.Now().UTC().Add(time.Duration(-10+i) * time.Minute)
				require.NoError(t, stores.Fixtures.SetExecutionStartedAt(ctx, run.ID, started))
				require.NoError(t, stores.Executions.Complete(ctx, run.ID, status, "done", "fixture failure", 10, 60000))
				require.NoError(t, stores.Fixtures.SetExecutionCompletedAt(ctx, run.ID, started.Add(time.Minute)))
				require.NoError(t, stores.Usage.RecordUsageEvent(ctx, &models.LLMUsageEvent{ProjectID: project.ID, TaskID: task.ID, ExecutionID: run.ID, AgentConfigID: config.ID, Provider: "test", Model: name, Operation: "task", InputTokens: 60, OutputTokens: 40, TotalTokens: 100, OccurredAt: started}))
				for _, event := range []string{models.SkillEventSelected, models.SkillEventLoaded} {
					require.NoError(t, stores.SkillAnalytics.RecordEvent(ctx, &models.SkillAnalyticsEvent{ProjectID: project.ID, TaskID: task.ID, ExecutionID: run.ID, ThreadID: run.ID, AgentID: agent.ID, SkillScope: models.SkillScopeGlobal, SkillHandle: "audit", EventType: event, CreatedAt: started}))
				}
			}
			schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce, RepeatInterval: 1}
			require.NoError(t, stores.Schedules.Create(ctx, schedule))
			definition, _, err := stores.Automations.PublishRegistered(ctx, models.AutomationRegisteredPublication{ProjectID: project.ID, StableKey: task.ID, Name: name + " automation", AutomationType: "scheduled", AdapterKey: "custom", CreatedVia: "test", Resources: []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"}}, Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}}})
			require.NoError(t, err)
			binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: definition.Nodes[0].ID}
			for i, state := range []models.AutomationTransitionState{"entered", "blocked"} {
				key := "event-" + strconv.Itoa(i)
				_, _, err := stores.Automations.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
					Context: models.AutomationContext{ProjectID: project.ID}, Binding: binding,
					WorkItemKey: "item", WorkItemKind: "task", WorkItemTitle: "Audit", WorkItemStatus: "blocked",
					ActivityKey: key, ActivityType: "task", ActivityStatus: "failed",
					EventKey: key, ToNodeID: binding.NodeID, Transition: state,
				})
				require.NoError(t, err)
				require.NoError(t, stores.Fixtures.SetAutomationTransitionTime(ctx, binding.AutomationID, key, time.Now().UTC().Add(time.Duration(-10+i)*time.Minute)))
			}
		}
	}
	runner := `<script>
(async()=>{
 const wait=async(fn)=>{const end=Date.now()+20000;while(!fn()){if(Date.now()>end)throw Error('timeout: '+fn);await new Promise(r=>setTimeout(r,30));}};
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 const chart=id=>window.Chart&&Chart.getChart&&Chart.getChart(document.getElementById(id));
 const sum=id=>chart(id).data.datasets.reduce((n,d)=>n+d.data.reduce((s,v)=>s+(typeof v==='number'?v:0),0),0);
 const inventory={
 overview:['projectOutcomeTrendChart'],
 outcomes:['successFailureChart','cycleDistributionChart','followupDistributionChart','hourlyTrendsChart','avgTimeTaskChart','failedPatternsChart'],
 agents:['agentComparisonChart','agentRunTimeChart','agentFollowupsChart'],
 models:['modelTaskTimeTrendChart','modelTimeChart','modelRunTimeTrendChart','modelRunTimeChart','modelTokenTrendChart','modelTokensChart','modelFollowupTrendChart','modelFollowupsChart','modelOutcomesChart','modelReliabilityChart'],
 automations:['automationComparisonChart'],
 learning:['skillUsageTrendChart','skillTopChart','skillFollowChart','skillAgentChart','skillOutcomeChart','skillEffectivenessChart'],
 usage:['usageRateChart','modelTokenBreakdownChart','usageRunHoursChart','usageRunModelsChart']};
 try {
 await wait(()=>chart('usageRunModelsChart'));
 for(const group of ['day','week','month']){
 for(const [view,ids] of Object.entries(inventory)) {
  history.pushState({},'','/analytics?project_id=__all_projects__&range='+(group==='week'?'7d':'all')+'&group_by='+group+'&view='+view);dispatchEvent(new PopStateEvent('popstate'));
  try {await wait(()=>ids.every(id=>chart(id)));} catch(e) {throw Error(view+': missing '+ids.filter(id=>!chart(id)).map(id=>id+' '+document.getElementById(id).parentElement.textContent).join('; '));}
  for(const id of ids){const c=chart(id);assert(c.data.datasets.length>0,id+' has no datasets');assert(c.data.datasets.some(d=>d.data.length>0),id+' has no data');}
  const expected={
   overview:{projectOutcomeTrendChart:1100/3},
   outcomes:{cycleDistributionChart:6,followupDistributionChart:6,hourlyTrendsChart:18,avgTimeTaskChart:6,failedPatternsChart:6},
   agents:{agentComparisonChart:200,agentRunTimeChart:360000,agentFollowupsChart:4},
   models:{modelTaskTimeTrendChart:1080000,modelTimeChart:1080000,modelRunTimeTrendChart:18,modelRunTimeChart:18,modelTokenTrendChart:1800,modelTokensChart:1800,modelFollowupTrendChart:12,modelFollowupsChart:12,modelOutcomesChart:200,modelReliabilityChart:200},
   automations:{automationComparisonChart:6},
   learning:{skillUsageTrendChart:36,skillTopChart:36,skillFollowChart:18,skillAgentChart:36,skillOutcomeChart:100},
   usage:{usageRateChart:1800,modelTokenBreakdownChart:1800,usageRunHoursChart:18,usageRunModelsChart:18}
  };
  // SQLite timestamp arithmetic can truncate less than one millisecond per run.
  for(const [id,value] of Object.entries(expected[view]))assert(Math.abs(sum(id)-value)<=(['agentRunTimeChart','modelTaskTimeTrendChart','modelTimeChart'].includes(id)?18:0.01),id+': expected '+value+', got '+sum(id));
  if(view==='outcomes'){
   const expected=[200/3,18,12,6,0];
   chart('successFailureChart').data.datasets.forEach((d,i)=>assert(Math.abs(d.data.reduce((n,v)=>n+v,0)-expected[i])<0.01,'run results series '+d.label));
  }
  if(view==='learning'){
   const points=chart('skillEffectivenessChart').data.datasets[0].data;
   assert(points.length===1&&points[0].x===6&&points[0].y===100,'skill effectiveness must cover six tasks');
  }
  if(view==='automations'){
   const options=Array.from(document.getElementById('analyticsWorkflowFilter').options).filter(o=>o.value);
   assert(options.length===6,'automation comparison must include both projects');
   for(const option of options){
    const ids=['automationFunnelChart','automationDurationChart','automationFailureChart','automationBottleneckChart'];
    const previous=chart(ids[0]);
    history.pushState({},'','/analytics?project_id=__all_projects__&range=all&group_by='+group+'&view=automations&workflow='+encodeURIComponent(option.value));dispatchEvent(new PopStateEvent('popstate'));
    await wait(()=>ids.every(id=>chart(id))&&chart(ids[0])!==previous);
    assert(sum(ids[0])===1,'one work item entered');
    assert(Math.abs(sum(ids[1])-60)<0.1,'one minute node duration');
    assert(sum(ids[2])===2,'two failed activities');
    assert(sum(ids[3])===1,'one blocked work item');
   }
  }
  const choices=view==='models'?[
   ['modelTaskTimeTrendSelect','modelTaskTimeTrendChart',1080000],
   ['modelRunTimeTrendSelect','modelRunTimeTrendChart',18],
   ['modelTokenTrendSelect','modelTokenTrendChart',1800],
   ['modelFollowupTrendSelect','modelFollowupTrendChart',12]
  ]:view==='usage'?[['usageRateModelSelect','usageRateChart',1800]]:[];
  for(const [selectID,chartID,total] of choices){
   const select=document.getElementById(selectID);
   const models=Array.from(select.options).filter(o=>o.value!=='combined'&&o.value!=='all');
   assert(models.length===2,selectID+' must offer both models');
   for(const option of models){
    select.value=option.value;select.dispatchEvent(new Event('change'));
    assert(Math.abs(sum(chartID)-total/2)<=(chartID==='modelTaskTimeTrendChart'?9:0.01),chartID+' selected model total');
   }
   for(const choice of ['all','combined']){
    select.value=choice;select.dispatchEvent(new Event('change'));
    assert(Math.abs(sum(chartID)-total)<=(chartID==='modelTaskTimeTrendChart'?18:0.01),chartID+' '+choice+' total');
   }
  }
  if(view==='models'){
   assert(sum('modelRunTimeChart')===18,'model runs must include both projects');
   assert(sum('modelTokensChart')===1800,'model tokens must include both projects');
   assert(sum('modelFollowupsChart')===12,'model followups must include both projects');
   assert(chart('modelReliabilityChart').data.labels.length===2,'both model reliability rows');
   for(const value of chart('modelReliabilityChart').data.datasets[0].data)assert(Math.abs(value-200/3)<0.001,'reliability expected 2/3');
  }
  if(view==='usage'){
   assert(sum('usageRunModelsChart')===18,'usage runs must include both projects');
   assert(sum('modelTokenBreakdownChart')===1800,'usage tokens must include both projects');
   assert(sum('usageRunHoursChart')===18,'run trend must include both projects');
  }
 }
 }
 // No UI addition: legacy views are exercised through their existing URLs.
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}
})();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 10000))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(204)
			return
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		body := rec.Body.String()
		if r.URL.Path == "/analytics" && !strings.EqualFold(r.Header.Get("HX-Request"), "true") {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	for _, size := range []string{"1280,900", "390,844"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--window-size="+size, server.URL+"/analytics?project_id=__all_projects__&range=all&view=usage")
			require.NoError(t, startHandlerBrowserProcess(t, cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-result:
				require.Equal(t, "pass", outcome)
			case <-time.After(90 * time.Second):
				t.Fatal("chart audit timed out")
			}
		})
	}
}
