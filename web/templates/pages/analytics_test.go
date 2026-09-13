package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestAnalyticsContent_LineChartHoverMarkerPaintsAfterTooltip(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}

	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	content := buf.String()
	for _, expected := range []string{
		`id: 'analyticsActivePointOnTop'`,
		`beforeEvent: function(chart, args)`,
		`chart.getElementsAtEventForMode(event, 'nearest', { intersect: false }, false)`,
		`afterDraw: function(chart)`,
		`data-analytics-hover-marker`,
		`pointerEvents = 'none'`,
		`zIndex = '2'`,
		`marker.style.clipPath = 'inset('`,
		`beforeDestroy: function(chart)`,
		`analyticsLineTooltipOptions()`,
		`itemSort: function(a, b)`,
		`position: 'nearest'`, `caretPadding: 6`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Analytics line-chart hover marker should paint after the tooltip; missing %q", expected)
		}
	}
	if got := strings.Count(content, `plugins: [analyticsActivePointOnTop]`); got != 3 {
		t.Fatalf("expected all 3 Analytics line charts to use the hover layering plugin, got %d", got)
	}
	for _, canvasID := range []string{"usageRateChart", "successFailureChart", "skillUsageTrendChart"} {
		expected := `<div class="relative h-64"><canvas id="` + canvasID + `"` // templ generation compacts adjacent markup.
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %s to have a positioned overlay container", canvasID)
		}
	}
}

func TestAnalyticsContent_LineChartHoverMarkerBehaviorInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><script>
(function() {
  function fail(message) {
    var result = document.getElementById('reconnect-result');
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', message);
    throw new Error(message);
  }
  window.fetch = function(url) {
    var value = String(url);
    var payload = [];
    if (value.indexOf('/api/analytics/skills') >= 0) payload = {usage_over_time: [{period: 'today', selected_count: 1}], top_skills: [], follow_through: [], agent_usage: {}, underused: []};
    if (value.indexOf('/api/analytics/usage') >= 0) payload = {usage_rate: [{period: 'today', total_tokens: 1}], usage_rate_by_model: [], totals: {}, model_breakdown: [], account_limits: []};
    if (value.indexOf('/api/analytics/success-failure-rates') >= 0) payload = [{Period: 'today', SuccessRate: 50, TotalCount: 2}];
    return Promise.resolve({ok: true, json: function() { return Promise.resolve(payload); }});
  };
  window.Chart = function(_canvasContext, config) {
    if (!config.plugins || config.plugins.length === 0) return;
    var plugin = config.plugins[0];
    var points = [
      {datasetIndex: 0, index: 0, element: {x: 40, y: 80, options: {backgroundColor: 'rgba(34, 197, 94, 0.12)'}}},
      {datasetIndex: 1, index: 0, element: {x: 40, y: 2, options: {backgroundColor: 'rgba(59, 130, 246, 0.12)'}}}
    ];
    var canvas = _canvasContext.canvas;
    canvas.getBoundingClientRect = function() { return {left: 10, top: 20, width: 200, height: 200, right: 210, bottom: 220}; };
    canvas.parentElement.getBoundingClientRect = function() { return {left: 10, top: 20, width: 200, height: 200, right: 210, bottom: 220}; };
    var chart = {
      canvas: canvas,
      width: 100,
      height: 100,
      data: {datasets: [
        {borderColor: 'rgb(34, 197, 94)'},
        {borderColor: 'rgb(59, 130, 246)'}
      ]},
      $analyticsHoveredPoint: null,
      chartArea: {left: 0, top: 0, right: 100, bottom: 100},
      tooltip: {opacity: 1},
      getElementsAtEventForMode: function(event, mode, options, useFinalPosition) {
        if (mode !== 'nearest' || options.intersect !== false || useFinalPosition !== false) fail('plugin did not request one pointer-nearest point');
        return event.y < 50 ? [points[1]] : [points[0]];
      }
    };
    var hoverArgs = {event: {type: 'mousemove', x: 40, y: 2}, inChartArea: true, changed: false};
    if (typeof plugin.beforeEvent !== 'function') fail('plugin does not track the pointer-nearest point before tooltip layout');
    plugin.beforeEvent(chart, hoverArgs);
    if (chart.$analyticsHoveredPoint !== points[1]) fail('plugin selected the wrong series point under index interaction');
    if (!hoverArgs.changed) fail('plugin did not schedule a redraw when the nearest series point changed');
    var tooltipOptions = config.options.plugins.tooltip;
    if (typeof tooltipOptions.itemSort !== 'function') fail('line tooltip does not promote the pointer-nearest series');
    var tooltipRows = [
      {datasetIndex: 0, chart: chart},
      {datasetIndex: 1, chart: chart},
      {datasetIndex: 2, chart: chart}
    ];
    tooltipRows.sort(tooltipOptions.itemSort);
    if (tooltipRows.map(function(item) { return item.datasetIndex; }).join(',') !== '1,0,2') fail('hovered series is not first while remaining tooltip rows preserve dataset order');
    plugin.afterDraw(chart);
    var marker = chart.canvas.parentElement.querySelector('[data-analytics-hover-marker]');
    if (!marker) fail('plugin did not create a DOM marker above the canvas tooltip');
    if (marker.style.pointerEvents !== 'none' || marker.style.zIndex !== '2') fail('DOM marker does not preserve pointer tracking or layer above the canvas');
    if (marker.style.left !== '80px' || marker.style.top !== '4px') fail('DOM marker is not responsively positioned over the selected point');
    var markerClipPath = getComputedStyle(marker).clipPath;
    if (markerClipPath === 'none' || markerClipPath.indexOf('2px') < 0) fail('DOM marker does not preserve chart-area clipping at an edge point: ' + markerClipPath);
    if (marker.style.backgroundColor !== 'rgb(59, 130, 246)' || marker.style.borderColor !== 'rgba(255, 255, 255, 0.96)') fail('DOM marker lacks an opaque dataset-colored center and contrasting halo');
    var outArgs = {event: {type: 'mouseout'}, inChartArea: false, changed: false};
    plugin.beforeEvent(chart, outArgs);
    plugin.afterDraw(chart);
    if (chart.$analyticsHoveredPoint !== null || !outArgs.changed || marker.style.display !== 'none') fail('plugin did not clear and hide the marker on mouseout');
    plugin.beforeDestroy(chart);
    if (chart.$analyticsHoverMarker !== null || marker.isConnected) fail('plugin did not remove the overlay marker when the chart was destroyed');
    window.__analyticsPluginChecks = (window.__analyticsPluginChecks || 0) + 1;
    if (window.__analyticsPluginChecks === 3) document.getElementById('reconnect-result').setAttribute('data-test-result', 'pass');
  };
})();
</script>` + rendered.String()

	runReconnectChromeFixture(t, fixture)
}

func TestAnalyticsContent_FiltersHistoryAndFailuresBehaviorInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
	  var result=document.getElementById('reconnect-result'), urls=[], dashboardCalls=0, evidencePageCalls=0, failDashboard=false, destroyed=0, chartsByID={};  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  function dashboard(url){var q=new URL(url,location.href).searchParams;return {definitions:[],current:{technical_completion:{numerator:1,denominator:1,percent:100},goal_achievement:{},first_pass:{numerator:1,denominator:1,percent:100},follow_up:{numerator:0,denominator:1,percent:0},tasks_evaluated:1,cycle_sample_size:1,cancelled_execution_count:0},funnel:[],cycle_distribution:[{label:'< 1m',count:1}],follow_up_distribution:[{label:'0',count:1}],agents:[{agent_id:'agent-1',agent_name:'Agent One',tasks_evaluated:1,technical_completion:{numerator:1,denominator:1,percent:100},goal_achievement:{},first_pass:{numerator:1,denominator:1,percent:100},follow_up:{numerator:0,denominator:1,percent:0}}],agent_detail:q.get('agent')?{outcome_trend:[{period:'today',completed:1,failed:0,cancelled:0,sample_size:1}],categories:[],model_mix:[],failures:[],skills:[],recent_tasks:[]}:null,skill_outcomes:[],model_categories:[{model_config_id:'model-1',model:'Model One',category:'active',tasks_evaluated:1,technical_completion:{numerator:1,denominator:1,percent:100}}],workflows:[{workflow_id:'workflow-1',workflow_name:'Workflow One',invocation_count:1,completed_count:1,failed_count:0}],workflow_detail:q.get('workflow')?{funnel:[],durations:[],failures:[],bottlenecks:[]}:null,evidence_total:2,evidence_limit:1,evidence_offset:Number(q.get('evidence_offset')||0),recent_outcomes:q.get('evidence_offset')==='1'?[{task_id:'created-task',task_title:'Created only',technical_result:'pending',goal_result:'',merge_state:'',agent_id:'',agent_name:'Unassigned',model:'Unknown',model_config_ids:[],terminal_period_statuses:[],category:'backlog',created_in_period:true,started_in_period:false,cycle_eligible:false,cycle_time_ms:0,execution_count:0,period_completed_count:0,period_failed_count:0,period_cancelled_count:0,follow_up_count:0,known_cost_usd:null}]:[{task_id:'retry-task',task_title:'Multi-model retry',technical_result:'completed',goal_result:'',merge_state:'',agent_id:'agent-1',agent_name:'Agent One',model:'Model Two',model_config_ids:['model-1','model-2'],terminal_period_statuses:['2026-01-10|failed'],category:'active',started_in_period:true,cycle_eligible:true,cycle_time_ms:1000,execution_count:2,period_completed_count:1,period_failed_count:1,period_cancelled_count:0,follow_up_count:1,known_cost_usd:null}],insights:[]};}
	  window.fetch=function(url){var value=String(url);urls.push(value);if(value.indexOf('/api/analytics/dashboard')>=0){dashboardCalls++;if(new URL(value,location.href).searchParams.get('evidence_offset')==='1'){evidencePageCalls++;return new Promise(function(resolve){setTimeout(function(){resolve({ok:true,json:function(){return Promise.resolve(dashboard(value));}});},50);});}if(failDashboard)return Promise.resolve({ok:false,status:500,json:function(){return Promise.resolve({});}});return Promise.resolve({ok:true,json:function(){return Promise.resolve(dashboard(value));}});}var payload=[];if(value.indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[{period:'2026-01-10',selected_count:1}],top_skills:[],follow_through:[],agent_usage:{cells:[],agents:[]},underused:[]};if(value.indexOf('/api/analytics/usage')>=0)payload={usage_rate:[{period:'2026-01-10',total_tokens:10}],usage_rate_by_model:[{period:'2026-01-10',provider:'test',model:'model-a',total_tokens:10}],totals:{call_count:1},model_breakdown:[{provider:'test',model:'model-a',total_tokens:10}],account_limits:[]};if(value.indexOf('/api/analytics/success-failure-rates')>=0)payload=[{Period:'2026-01-10',TotalCount:2,SuccessCount:1,FailureCount:1,CancelledCount:0,SuccessRate:50}];if(value.indexOf('/api/analytics/execution-trends-by-hour')>=0)payload=[{Hour:10,Count:1}];if(value.indexOf('/api/analytics/avg-execution-time-by-agent')>=0)payload=[{ID:'model-1',Name:'Model One',AvgMs:1000,Count:1}];if(value.indexOf('/api/analytics/agent-usage-by-project')>=0)payload=[{AgentID:'model-1',AgentName:'Model One',ExecutionCount:1}];return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};	  window.Chart=function(ctx,config){this.config=config;this.destroy=function(){destroyed++;};if(ctx&&ctx.canvas)chartsByID[ctx.canvas.id]=config;};  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out');setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){
		    waitFor(function(){return dashboardCalls>0&&document.querySelector('#analyticsAgentFilter option[value="agent-1"]')&&chartsByID.cycleDistributionChart&&chartsByID.successFailureChart&&chartsByID.modelTokenBreakdownChart&&chartsByID.avgTimeAgentChart&&chartsByID.agentUsageChart&&document.querySelector('[data-model-evidence]');},function(){
		      var more=document.getElementById('loadMoreEvidence');
		      if(more.classList.contains('hidden')||more.textContent.indexOf('1 of 2')<0)fail('evidence pagination is not disclosed');
			      more.click();
			      more.click();
			      waitFor(function(){return document.getElementById('outcomeEvidenceTable').textContent.indexOf('Created only')>=0;},function(){
			      if(evidencePageCalls!==1)fail('rapid Load More clicks requested duplicate evidence pages: '+evidencePageCalls);		      if(document.getElementById('outcomeEvidenceTable').textContent.indexOf('Unavailable')<0||!more.classList.contains('hidden'))fail('loaded nonterminal evidence did not render unavailable cycle and complete pagination');
		      chartsByID.modelTokenBreakdownChart.options.onClick({},[{index:0}]);
		      if(!document.querySelector('#usageBreakdownTable [data-usage-model]').classList.contains('bg-primary/10'))fail('token breakdown did not identify its supporting usage row');
		      chartsByID.avgTimeAgentChart.options.onClick({},[{index:0}]);
		      if(new URLSearchParams(location.search).get('model_id')!=='model-1')fail('model duration did not open stable model evidence');
		      chartsByID.agentUsageChart.options.onClick({},[{index:0}]);
		      if(new URLSearchParams(location.search).get('model_id')!=='model-1')fail('model execution share did not open stable model evidence');
		      chartsByID.cycleDistributionChart.options.onClick({},[{index:0}]);	      var drilldown=new URLSearchParams(location.search);
	      if(drilldown.get('view')!=='outcomes'||drilldown.get('evidence')!=='cycleDistribution'||drilldown.get('cycleDistribution')!=='< 1m')fail('chart drill-down did not persist evidence state');
	      if(document.getElementById('outcomeEvidenceFilter').textContent.indexOf('cycleDistribution')<0)fail('chart drill-down did not render filtered evidence');
	      document.querySelector('[data-model-evidence]').click();
	      drilldown=new URLSearchParams(location.search);
	      if(drilldown.get('model_id')!=='model-1'||drilldown.get('category')!=='active'||drilldown.has('cycleDistribution'))fail('model/category drill-down omitted its selected subset or retained stale evidence');
	      if(document.getElementById('outcomeEvidenceTable').textContent.indexOf('Multi-model retry')<0)fail('model drill-down lost task whose latest model differs');
	      chartsByID.successFailureChart.options.onClick({},[{index:0,datasetIndex:3}]);
	      drilldown=new URLSearchParams(location.search);
	      if(drilldown.get('technical_status')!=='failed'||drilldown.get('period')!=='2026-01-10'||drilldown.has('model_id')||drilldown.has('category'))fail('technical trend drill-down omitted status/period or retained stale evidence');
	      var agent=document.getElementById('analyticsAgentFilter');agent.value='agent-1';agent.dispatchEvent(new Event('change'));      waitFor(function(){return urls.some(function(u){return u.indexOf('/api/analytics/dashboard')>=0&&u.indexOf('agent=agent-1')>=0;})&&urls.some(function(u){return u.indexOf('success-failure-rates')>=0&&u.indexOf('agent=agent-1')>=0;});},function(){
        var workflow=document.getElementById('analyticsWorkflowFilter');workflow.value='workflow-1';workflow.dispatchEvent(new Event('change'));
        waitFor(function(){return urls.some(function(u){return u.indexOf('/api/analytics/dashboard')>=0&&u.indexOf('workflow=workflow-1')>=0;});},function(){
          history.back();
          waitFor(function(){return document.getElementById('analyticsWorkflowFilter').value===''&&document.getElementById('analyticsAgentFilter').value==='agent-1';},function(){
            failDashboard=true;document.getElementById('refreshBtn').click();
            waitFor(function(){return !document.getElementById('analyticsError').classList.contains('hidden');},function(){if(destroyed<1)fail('stale charts were not destroyed on failure');result.setAttribute('data-test-result','pass');});
          });
        });
	      });
	    });
	  });
	});
})();</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestAnalyticsContent_DirectFilteredURLAppliesFirstRequestsInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=agents&range=30d&agent=agent-1&workflow=workflow-1');
  var result=document.getElementById('reconnect-result'),urls=[];
  function dashboard(){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[{agent_id:'agent-1',agent_name:'Agent One',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],skill_outcomes:[],model_categories:[],workflows:[{workflow_id:'workflow-1',workflow_name:'Workflow One'}],recent_outcomes:[],insights:[]};}
  window.Chart=function(){this.destroy=function(){};};
  window.fetch=function(url){var value=String(url);urls.push(value);result.setAttribute('data-observed-urls',urls.join('|'));var payload=[];if(value.indexOf('/api/analytics/dashboard')>=0)payload=dashboard();if(value.indexOf('/api/analytics/usage')>=0)payload={usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[]};if(value.indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[],top_skills:[],follow_through:[],agent_usage:{},underused:[]};return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};
  function wait(){var required=['/api/analytics/dashboard','/api/analytics/usage','/api/analytics/skills','success-failure-rates'];var ready=required.every(function(path){return urls.some(function(url){return url.indexOf(path)>=0&&url.indexOf('agent=agent-1')>=0&&url.indexOf('workflow=workflow-1')>=0;});});if(ready&&document.getElementById('analyticsAgentFilter').value==='agent-1'&&document.getElementById('analyticsWorkflowFilter').value==='workflow-1'){result.setAttribute('data-test-result','pass');return;}setTimeout(wait,20);}
  window.addEventListener('load',wait);
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestAnalyticsContent_DelayedChartLoaderInitializesNewestGenerationOnceInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	content := rendered.String()
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  var result=document.getElementById('reconnect-result'),loader=null,dashboardCalls=0,append=document.head.appendChild.bind(document.head);
  document.head.appendChild=function(node){if(node&&node.matches&&node.matches('script[data-analytics-chart-loader]')){loader=node;node.removeAttribute('src');return append(node);}return append(node);};
  window.fetch=function(url){if(String(url).indexOf('/api/analytics/dashboard')>=0)dashboardCalls++;var payload=[];if(String(url).indexOf('/api/analytics/dashboard')>=0)payload={definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[],skill_outcomes:[],model_categories:[],workflows:[],recent_outcomes:[],insights:[]};if(String(url).indexOf('/api/analytics/usage')>=0)payload={usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[]};if(String(url).indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[],top_skills:[],follow_through:[],agent_usage:{},underused:[]};return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};
  window.__finishAnalyticsLoaderTest=function(){if(!loader){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error','loader missing');return;}window.Chart=function(){this.destroy=function(){};};loader.dispatchEvent(new Event('load'));setTimeout(function(){if(dashboardCalls!==1){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error','dashboard initialized '+dashboardCalls+' times');return;}if(document.querySelectorAll('script[data-analytics-chart-loader]').length!==1){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error','duplicate loaders');return;}result.setAttribute('data-test-result','pass');},100);};
})();
</script>` + content + content + `<script>window.__finishAnalyticsLoaderTest();</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestAgentsContent_OpensProjectScopedAgentFromAnalyticsLink(t *testing.T) {
	var rendered bytes.Buffer
	agent := models.Agent{ID: "agent-1", Name: "Agent One", Model: "inherit", Enabled: true}
	if err := AgentsContent([]models.Agent{agent}, nil).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render Agents content: %v", err)
	}
	content := rendered.String()
	for _, expected := range []string{"get('agent_id')", "card.dataset.agentId === selectedAgentID", "editAgentFromData(selectedCard)"} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Agents page does not hydrate Analytics agent detail link; missing %q", expected)
		}
	}
}

func TestAnalyticsContent_TokenUsageModelSelectStaysWithinCard(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}

	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	content := buf.String()
	required := []string{
		`<div class="flex flex-wrap items-end justify-between gap-3 mb-2 min-w-0">`,
		`<div class="form-control min-w-0 w-full sm:w-auto">`,
		`id="usageRateModelSelect" class="select select-bordered select-xs w-full max-w-full sm:min-w-48"`,
	}
	for _, expected := range required {
		if !strings.Contains(content, expected) {
			t.Fatalf("Token Usage model select should stay within its card on narrow screens; missing %q", expected)
		}
	}
	if strings.Contains(content, `id="usageRateModelSelect" class="select select-bordered select-xs min-w-48"`) {
		t.Fatal("Token Usage model select should not force a fixed minimum width on mobile")
	}
}

func TestAnalyticsContent_HasPersistentViewsDefinitionsAndSafeRendering(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	content := buf.String()
	for _, expected := range []string{
		`data-analytics-view="overview"`, `data-analytics-view="outcomes"`,
		`data-analytics-view="agents"`, `data-analytics-view="workflows"`,
		`data-analytics-view="learning"`, `data-analytics-view="usage"`,
		`data-analytics-view="all"`, `data-analytics-section="overview"`,
		`data-analytics-section="outcomes"`, `data-analytics-section="agents"`,
		`data-analytics-section="workflows"`, `id="analyticsJumpLinks"`,
		`Technical completion rate`, `Goal achievement rate`, `Technical first-pass rate`,
		`Follow-up rate`, `Median task cycle time`, `Cost per achieved goal`,
		`Recent outcomes`, `Outcome funnel`, `Task-level outcome evidence`, `Agent performance`, `Workflow performance`,
		`Observed outcomes among tasks using skills`, `Observed outcomes by Agent and skill`, `Current account state · not date-filtered`,
		`Selected Agent outcome trend`, `Performance by task category`, `Selected Agent model mix`, `Recurring failures`,
		`Node funnel`, `Node durations`, `Node failures`, `Current bottlenecks`, `Model performance by task category`,
		`Technical Execution Completion Over Time`, `Memory effectiveness unavailable`, `id="loadMoreEvidence"`, `loaded ' + recent.length + ' of '`, `row.cycle_eligible ? formatDuration`, `row.duration_sample_size`, `focusUsageEvidence`, `window.location.href='/tasks?project_id='`, `window.location.href='/skills?project_id='`,
		`history.replaceState`, `history.pushState`, `params.set('view'`, `params.set('agent'`, `params.set('workflow'`, `params.set('evidence', key)`, `navigateEvidence('outcomes','model_id'`, `window.addEventListener('popstate'`, `renderChartState`, `destroyChart`, `escapeHTML(task.TaskTitle`, `canvas.setAttribute('aria-label'`,
		`table.innerHTML = '<tr><td colspan="' + item[1] + '" class="text-center opacity-50">Analytics unavailable</td></tr>'`,
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("analytics outcome dashboard missing %q", expected)
		}
	}
	if strings.Contains(content, `>${task.TaskTitle || 'Unknown'}<`) || strings.Contains(content, `<td>${pattern.TaskTitle || 'Unknown'}</td>`) {
		t.Fatal("dynamic task titles must be escaped before innerHTML insertion")
	}
}

func TestAnalyticsContent_AllMetricsPreservesExistingMetrics(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	content := buf.String()
	for _, metric := range []string{
		"Token Usage", "Model Breakdown by Tokens", "Token Usage Breakdown",
		"Task Execution by Hour", "Average Execution Time by Task", "Average Execution Time by Model",
		"Model Breakdown by Executions", "Most Frequently Run Tasks", "Skill Activity Over Time",
		"Top Skills", "Follow-through / Selected Outcomes", "Top Agent/Skill Pairs",
		"Least Active Enabled Skills", "Observed outcomes among tasks using skills", "Model performance by task category", "Failed Task Patterns", "accountUsageCards",
	} {
		if !strings.Contains(content, metric) {
			t.Errorf("All Metrics lost existing metric %q", metric)
		}
	}
}
