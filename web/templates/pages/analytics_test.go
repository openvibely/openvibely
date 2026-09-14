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

func TestAnalyticsContent_RejectsStaleDashboardAndEvidenceResponsesInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
	  var result=document.getElementById('reconnect-result'),staleResolve=null,staleSignal=null,agentBaseResolve=null,agentPageResolve=null,prematureAgentPageCalls=0;  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  function dashboard(agent,title,total){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{},tasks_evaluated:agent?2:1},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[{agent_id:'agent-2',agent_name:'Agent Two',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:total,evidence_limit:20,evidence_offset:0,recent_outcomes:[{task_id:title,task_title:title,technical_result:'running',goal_result:'',merge_state:'',agent_id:agent,agent_name:agent?'Agent Two':'Unassigned',model:'Model',model_config_ids:[],terminal_period_statuses:[],category:'active',started_in_period:true,cycle_eligible:false,execution_count:1,period_completed_count:0,period_failed_count:0,period_cancelled_count:0,follow_up_count:0}],insights:[]};}
  window.Chart=function(){this.destroy=function(){};};
  window.fetch=function(url,options){var value=String(url),q=new URL(value,location.href).searchParams,payload=[];
    if(value.indexOf('/api/analytics/dashboard')>=0){
	      if(q.get('evidence_offset')==='1'&&!q.get('agent')){staleSignal=options&&options.signal;return new Promise(function(resolve){staleResolve=function(){resolve({ok:true,json:function(){return Promise.resolve(dashboard('','Stale evidence',2));}});};});}
	      if(q.get('agent')==='agent-2'&&q.get('evidence_offset')==='0'){return new Promise(function(resolve){agentBaseResolve=function(){resolve({ok:true,json:function(){return Promise.resolve(dashboard('agent-2','Agent Two evidence',2));}});};});}
	      if(q.get('agent')==='agent-2'&&q.get('evidence_offset')==='1'){prematureAgentPageCalls++;return new Promise(function(resolve){agentPageResolve=function(){resolve({ok:true,json:function(){return Promise.resolve(dashboard('agent-2','Agent Two page two',2));}});};});}
	      payload=dashboard('','Initial evidence',2);    }
    if(value.indexOf('/api/analytics/usage')>=0)payload={usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[]};
    if(value.indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[],top_skills:[],follow_through:[],agent_usage:{cells:[],agents:[]},underused:[]};
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out');setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){
    waitFor(function(){return document.getElementById('outcomeEvidenceTable').textContent.indexOf('Initial evidence')>=0;},function(){
      document.getElementById('loadMoreEvidence').click();
      waitFor(function(){return staleResolve!==null;},function(){
	        var agent=document.getElementById('analyticsAgentFilter');agent.value='agent-2';agent.dispatchEvent(new Event('change'));
	        document.getElementById('loadMoreEvidence').click();
	        setTimeout(function(){
	          if(prematureAgentPageCalls!==0)fail('stale evidence control requested a new-filter page before its base dashboard');
	          agentBaseResolve();
	          waitFor(function(){return document.getElementById('outcomeEvidenceTable').textContent.indexOf('Agent Two evidence')>=0;},function(){
	            var more=document.getElementById('loadMoreEvidence');more.click();
	            waitFor(function(){return agentPageResolve!==null;},function(){
	              staleResolve();
	              setTimeout(function(){
	                if(!more.disabled)fail('stale evidence finalizer re-enabled a current-generation page request');
	                agentPageResolve();
	                waitFor(function(){return document.getElementById('outcomeEvidenceTable').textContent.indexOf('Agent Two page two')>=0;},function(){
	                  var text=document.getElementById('outcomeEvidenceTable').textContent;
	                  if(text.indexOf('Stale evidence')>=0||text.indexOf('Agent Two evidence')<0)fail('stale evidence contaminated the selected Agent dashboard');
	                  if(!staleSignal||!staleSignal.aborted)fail('previous Analytics request generation was not aborted');
	                  if(!document.getElementById('analyticsError').classList.contains('hidden'))fail('aborted stale request surfaced as an Analytics error');
	                  result.setAttribute('data-test-result','pass');
	                });
	              },40);
	            });
	          });
	        },40);      });
    });
  });
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestAnalyticsContent_RejectsStaleEvidenceErrorsAndClearsChartsOnFilterChangeInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  var result=document.getElementById('reconnect-result'),charts={},destroyed=[],rejectOldSkill=null,rejectOldUsage=null,agentDashboardResolve=null;
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  function dashboard(){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[{label:'< 1m',count:1}],follow_up_distribution:[],agents:[{agent_id:'agent-1',agent_name:'Agent One',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:0,evidence_limit:20,evidence_offset:0,recent_outcomes:[],insights:[]};}
  function skills(query){return {usage_over_time:[{period:'2026-01-10',created_count:1},{period:'2026-01-11',created_count:1}],top_skills:[],follow_through:[],agent_usage:{agents:[],cells:[]},underused:[],evidence_total:query.get('skill_period')?1:0,evidence:query.get('skill_period')?[{id:'skill-'+query.get('skill_period'),created_at:'2026-01-11T00:00:00Z',skill_handle:'project:review',event_type:'created',source:'manual',surface:'task_thread'}]:[]};}
  function usage(query){return {usage_rate:[{period:'2026-01-10',total_tokens:1},{period:'2026-01-11',total_tokens:2}],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[],evidence_total:query.get('usage_period')||query.get('usage_provider')?1:0,evidence:query.get('usage_period')||query.get('usage_provider')?[{id:'usage-'+query.get('usage_period'),occurred_at:'2026-01-11T00:00:00Z',provider:'test',model:'model-a',total_tokens:2}]:[]};}
  window.Chart=function(ctx,config){var id=ctx&&ctx.canvas?ctx.canvas.id:'';charts[id]=config;this.destroy=function(){destroyed.push(id);};};
  window.fetch=function(url){var value=String(url),query=new URL(value,location.href).searchParams,payload=[];
    if(value.indexOf('/api/analytics/dashboard')>=0){if(query.get('agent')==='agent-1')return new Promise(function(resolve){agentDashboardResolve=function(){resolve({ok:true,json:function(){return Promise.resolve(dashboard());}});};});payload=dashboard();}
    if(value.indexOf('/api/analytics/skills')>=0){if(query.get('skill_period')==='2026-01-10')return new Promise(function(_,reject){rejectOldSkill=reject;});payload=skills(query);}
    if(value.indexOf('/api/analytics/usage')>=0){if(query.get('usage_period')==='2026-01-10')return new Promise(function(_,reject){rejectOldUsage=reject;});payload=usage(query);}
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out');setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){waitFor(function(){return charts.skillUsageTrendChart&&charts.usageRateChart&&charts.cycleDistributionChart;},function(){
    charts.skillUsageTrendChart.options.onClick({},[{index:0,datasetIndex:1}]);
    waitFor(function(){return rejectOldSkill!==null;},function(){
      charts.skillUsageTrendChart.options.onClick({},[{index:1,datasetIndex:1}]);
      waitFor(function(){return document.getElementById('skillEventEvidenceTable').textContent.indexOf('skill-2026-01-11')>=0;},function(){
        rejectOldSkill(new Error('old selection failed'));
        setTimeout(function(){
          if(!document.getElementById('analyticsError').classList.contains('hidden'))fail('stale skill evidence failure replaced the newer selection');
          if(document.getElementById('skillEventEvidenceTable').textContent.indexOf('skill-2026-01-11')<0)fail('stale skill evidence failure cleared newer records');
          charts.usageRateChart.options.onClick({},[{index:0,datasetIndex:0}]);
          waitFor(function(){return rejectOldUsage!==null;},function(){
            charts.usageRateChart.options.onClick({},[{index:1,datasetIndex:0}]);
            waitFor(function(){return document.getElementById('usageEventEvidenceTable').textContent.indexOf('usage-2026-01-11')>=0;},function(){
              rejectOldUsage(new Error('old usage selection failed'));
              setTimeout(function(){
                if(!document.getElementById('analyticsError').classList.contains('hidden'))fail('stale usage evidence failure replaced the newer selection');
                if(document.getElementById('usageEventEvidenceTable').textContent.indexOf('usage-2026-01-11')<0)fail('stale usage evidence failure cleared newer records');
                var agent=document.getElementById('analyticsAgentFilter');agent.value='agent-1';agent.dispatchEvent(new Event('change'));
                setTimeout(function(){
                  if(destroyed.indexOf('cycleDistributionChart')<0)fail('filter change left the old chart visible');
                  var state=document.querySelector('#cycleDistributionChart + [data-analytics-chart-state], #cycleDistributionChart ~ [data-analytics-chart-state]');
                  if(!state||state.textContent.indexOf('Loading')<0)fail('filter change did not show a controlled loading state');
                  agentDashboardResolve();
                  result.setAttribute('data-test-result','pass');
                },20);
              },40);
            });
          });
        },40);
      });
    });
  });});
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestAnalyticsContent_ChartInteractionsPreserveExactSupportingSubsetInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
	  var result=document.getElementById('reconnect-result'),charts={},urls=[],longHandle='shared:review-skill-with-a-very-long-identical-handle';  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  window.Chart=function(ctx,config){this.destroy=function(){};if(ctx&&ctx.canvas)charts[ctx.canvas.id]=config;};
	  window.fetch=function(url){var value=String(url),query=new URL(value,location.href).searchParams,payload=[];urls.push(value);
	    if(value.indexOf('/api/analytics/dashboard')>=0)payload={definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:1,evidence_limit:20,evidence_offset:0,recent_outcomes:[{task_id:'hour-task',task_title:'Hour task',technical_result:'running',goal_result:'',merge_state:'',agent_id:'agent-1',agent_name:'Agent One',model:'Model',model_config_ids:[],execution_hours:[10],terminal_period_statuses:[],category:'active',started_in_period:true,cycle_eligible:false,execution_count:1,period_completed_count:0,period_failed_count:0,period_cancelled_count:0,follow_up_count:0}],insights:[]};
	    if(value.indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[{period:'2026-01-10',selected_count:1,loaded_count:0,viewed_count:0,created_count:2,edited_count:0}],top_skills:[{skill_handle:longHandle,skill_scope:'project',selected_count:1,loaded_count:1,viewed_count:0,created_count:2,edited_count:0},{skill_handle:longHandle,skill_scope:'global',selected_count:0,loaded_count:0,viewed_count:0,created_count:1,edited_count:0}],follow_through:[{skill_handle:longHandle,skill_scope:'project',selected_count:2,loaded_or_viewed_count:1,ignored_count:1},{skill_handle:longHandle,skill_scope:'global',selected_count:1,loaded_or_viewed_count:0,ignored_count:1}],agent_usage:{agents:[{agent_id:'agent-1',agent_name:'Agent One'}],cells:[{agent_id:'agent-1',skill_handle:longHandle,skill_scope:'project',selected_count:1,loaded_count:0,viewed_count:0},{agent_id:'agent-1',skill_handle:longHandle,skill_scope:'global',selected_count:1,loaded_count:0,viewed_count:0}]},underused:[],evidence_total:query.get('skill_period')||query.get('skill_handle')?1:0,evidence:query.get('skill_period')||query.get('skill_handle')?[{id:'skill-event-1',created_at:'2026-01-10T10:00:00Z',task_id:'skill-task',execution_id:'skill-exec',agent_id:'agent-1',agent_name:'Agent One',skill_handle:query.get('skill_handle')||'project:created',skill_scope:query.get('skill_evidence_scope')||'project',event_type:query.get('skill_event')||'selected',source:'manual',surface:'task_thread'}]:[]};
	    if(value.indexOf('/api/analytics/usage')>=0)payload={usage_rate:[{period:'2026-01-10',total_tokens:10}],usage_rate_by_model:[{period:'2026-01-10',provider:'test',model:'model-a',total_tokens:10}],totals:{call_count:1},model_breakdown:[{provider:'test',model:'model-a',total_tokens:10}],account_limits:[],evidence_total:query.get('usage_period')||query.get('usage_provider')?1:0,evidence:query.get('usage_period')||query.get('usage_provider')?[{id:'usage-event-1',occurred_at:'2026-01-10T11:00:00Z',provider:'test',model:'model-a',task_id:'usage-task',execution_id:'usage-exec',total_tokens:10,cost_usd:0.01}]:[]};    if(value.indexOf('/api/analytics/execution-trends-by-hour')>=0)payload=[{Hour:10,Count:1}];
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
	  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out; urls='+urls.join('|')+'; skill='+document.getElementById('skillEventEvidenceTable').textContent+'; usage='+document.getElementById('usageEventEvidenceTable').textContent);setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
	  window.addEventListener('load',function(){waitFor(function(){return charts.hourlyTrendsChart&&charts.skillUsageTrendChart&&charts.skillTopChart&&charts.skillFollowChart&&charts.skillAgentChart&&charts.usageRateChart&&charts.modelTokenBreakdownChart;},function(){    charts.hourlyTrendsChart.options.onClick({},[{index:10}]);
    var params=new URLSearchParams(location.search);
    if(params.get('evidence')!=='execution_hour'||params.get('execution_hour')!=='10'||document.getElementById('outcomeEvidenceTable').textContent.indexOf('Hour task')<0)fail('hour click did not filter exact task evidence');
	    charts.skillUsageTrendChart.options.onClick({},[{index:0,datasetIndex:1}]);
	    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/skills')>=0&&url.indexOf('skill_period=2026-01-10')>=0&&url.indexOf('skill_event=created')>=0;})&&document.getElementById('skillEventEvidenceTable').textContent.indexOf('skill-event-1')>=0;},function(){
	    params=new URLSearchParams(location.search);
	    if(params.get('skill_period')!=='2026-01-10'||params.get('skill_event')!=='created'||document.getElementById('skillEvidenceSelection').textContent.indexOf('Created')<0)fail('skill trend click lost period, dataset, or supporting records');
	    charts.skillAgentChart.options.onClick({},[{index:0,datasetIndex:0}]);
		    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/skills')>=0&&url.indexOf('skill_agent=agent-1')>=0&&url.indexOf('skill_handle='+encodeURIComponent(longHandle))>=0&&url.indexOf('skill_evidence_scope=project')>=0&&url.indexOf('skill_event=used')>=0;})&&document.getElementById('skillEventEvidenceTable').textContent.indexOf(longHandle)>=0;},function(){	    params=new URLSearchParams(location.search);
	    if(params.get('skill_agent')!=='agent-1'||params.get('skill_handle')!==longHandle||params.get('skill_evidence_scope')!=='project'||charts.skillAgentChart.data.labels[0].indexOf('[project]')<0||charts.skillAgentChart.data.labels[1].indexOf('[global]')<0)fail('Agent-skill click lost scoped pair identity, visible scope, or supporting records');
	    charts.usageRateChart.options.onClick({},[{index:0,datasetIndex:0}]);
		    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/usage')>=0&&url.indexOf('usage_period=2026-01-10')>=0;})&&document.getElementById('usageEventEvidenceTable').textContent.indexOf('usage-event-1')>=0;},function(){	    params=new URLSearchParams(location.search);
	    if(params.get('usage_period')!=='2026-01-10'||params.get('usage_model')!=='combined'||document.getElementById('usageEvidenceSelection').textContent.indexOf('2026-01-10')<0)fail('token trend click lost period/model aggregate or supporting records');
		    charts.skillTopChart.options.onClick({},[{index:0,datasetIndex:3}]);
		    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/skills')>=0&&url.indexOf('skill_handle='+encodeURIComponent(longHandle))>=0&&url.indexOf('skill_evidence_scope=project')>=0&&url.indexOf('skill_event=created')>=0;})&&document.getElementById('skillEventEvidenceTable').textContent.indexOf('project')>=0;},function(){
		      var skillParams=new URLSearchParams(location.search);
		      if(skillParams.get('skill_evidence_scope')!=='project'||charts.skillTopChart.data.labels[0].indexOf('[project]')<0||charts.skillTopChart.data.labels[1].indexOf('[global]')<0)fail('Top Skills click lost scope identity or rendered ambiguous long duplicate labels');
		      charts.skillFollowChart.options.onClick({},[{index:1,datasetIndex:1}]);
		      waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/skills')>=0&&url.indexOf('skill_handle='+encodeURIComponent(longHandle))>=0&&url.indexOf('skill_evidence_scope=global')>=0&&url.indexOf('skill_event=ignored')>=0;})&&document.getElementById('skillEventEvidenceTable').textContent.indexOf('global')>=0;},function(){	        charts.modelTokenBreakdownChart.options.onClick({},[{index:0}]);
	        waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/usage')>=0&&url.indexOf('usage_provider=test')>=0&&url.indexOf('usage_model_name=model-a')>=0;})&&document.getElementById('usageEventEvidenceTable').textContent.indexOf('usage-event-1')>=0;},function(){result.setAttribute('data-test-result','pass');});
	      });
	    });
	    });
	    });
	    });  });});
})();
</script>` + rendered.String()
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
		`Technical Execution Completion Over Time`, `Memory effectiveness unavailable`, `id="loadMoreEvidence"`, `loaded ' + recent.length + ' of '`, `row.cycle_eligible ? formatDuration`, `row.duration_sample_size`, `focusUsageEvidence`, `navigateEvidence('outcomes','execution_hour'`, `id="skillEvidenceSelection"`, `id="usageEvidenceSelection"`, `skill_event:eventType`, `loadSkillEvidence()`, `showUsageModelEvidence`, `history.replaceState`, `history.pushState`, `params.set('view'`, `params.set('agent'`, `params.set('workflow'`, `params.set('evidence', key)`, `navigateEvidence('outcomes','model_id'`, `window.addEventListener('popstate'`, `renderChartState`, `destroyChart`, `escapeHTML(task.TaskTitle`, `canvas.setAttribute('aria-label'`,
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
