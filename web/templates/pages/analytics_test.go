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
	for canvasID, height := range map[string]string{"usageRateChart": "h-64", "successFailureChart": "h-72", "skillUsageTrendChart": "h-64"} {
		expected := `<div class="relative ` + height + `"><canvas id="` + canvasID + `"` // templ generation compacts adjacent markup.
		if !strings.Contains(content, expected) {
			t.Fatalf("expected %s to have a positioned overlay container", canvasID)
		}
	}
}

func TestBrowserFunctional_AnalyticsContent_LineChartHoverMarkerBehaviorInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><script>
(function() {
  history.replaceState({}, '', location.pathname + '?project_id=project-1&view=outcomes');
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
    if (window.__analyticsPluginChecks === 1) document.getElementById('reconnect-result').setAttribute('data-test-result', 'pass');
  };
})();
</script>` + rendered.String()

	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_KPIsFunnelsAndChartsAreDisplayOnlyInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=overview');
  var result=document.getElementById('reconnect-result'),charts={},destroyed=0,scrollCalls=0,dashboardRequests=0;
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  Element.prototype.scrollIntoView=function(){scrollCalls++;};
  window.Chart=function(ctx,config){var id=ctx&&ctx.canvas?ctx.canvas.id:'';this.config=config;this.destroy=function(){destroyed++;};if(id)charts[id]=this;};
  function dashboard(){return {definitions:[],current:{technical_completion:{numerator:1,denominator:1,percent:100},goal_achievement:{numerator:1,denominator:1,percent:100},first_pass:{numerator:1,denominator:1,percent:100},follow_up:{numerator:0,denominator:1,percent:0},tasks_evaluated:1},outcome_trend:[{period:'2026-01-10',technical_completion:{percent:100,denominator:1},goal_achievement:{percent:100,denominator:1},first_pass:{percent:100,denominator:1},follow_up:{percent:0,denominator:1}}],funnel:[{key:'created',label:'Created',count:1,denominator:1}],cycle_distribution:[],follow_up_distribution:[],agents:[],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:1,evidence_limit:20,evidence_offset:0,recent_outcomes:[{task_id:'task-1',task_title:'Task One',technical_result:'completed',goal_result:'achieved',merge_state:'',agent_name:'Agent',model:'Model',model_config_ids:[],terminal_period_statuses:['2026-01-10|completed'],category:'completed',created_in_period:true,started_in_period:true,first_pass_eligible:true,first_pass_completed:true,goal_achievement_eligible:true,goal_achieved_in_period:true,cycle_eligible:true,cycle_time_ms:1000,execution_count:1,period_completed_count:1,period_failed_count:0,period_cancelled_count:0,follow_up_count:0}],insights:[]};}
  window.fetch=function(url){var value=String(url),payload=[];if(value.indexOf('/api/analytics/dashboard')>=0){dashboardRequests++;payload=dashboard();}if(value.indexOf('/api/analytics/usage')>=0)payload={usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[]};if(value.indexOf('/api/analytics/execution-trends-by-hour')>=0)payload=[{Hour:10,Count:1}];return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out');setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){waitFor(function(){return document.querySelector('#analyticsKpis .card')&&charts.projectOutcomeTrendChart;},function(){
    if(document.querySelector('#analyticsKpis a,[data-analytics-evidence-link]'))fail('KPI or funnel rendered as a link');
    if(typeof charts.projectOutcomeTrendChart.config.options.onClick==='function')fail('overview chart is clickable');
    history.pushState({},'',location.pathname+'?project_id=project-1&view=outcomes');window.dispatchEvent(new PopStateEvent('popstate'));
    waitFor(function(){return charts.hourlyTrendsChart&&document.querySelector('#outcomeSummaryCards .card');},function(){
      if(document.querySelector('#outcomeSummaryCards a,#outcomeFunnel a'))fail('outcome KPI or funnel rendered as a link');
      Object.keys(charts).forEach(function(id){if(charts[id].config.options&&typeof charts[id].config.options.onClick==='function')fail(id+' chart is clickable');});
      var url=location.href,requests=dashboardRequests,destroyedBefore=destroyed;
      document.querySelector('#outcomeSummaryCards .card').click();
      document.getElementById('hourlyTrendsChart').dispatchEvent(new MouseEvent('click',{bubbles:true}));
      setTimeout(function(){
        if(location.href!==url)fail('display-only visualization changed the URL');
        if(dashboardRequests!==requests)fail('display-only visualization reloaded dashboard data');
        if(destroyed!==destroyedBefore)fail('display-only visualization destroyed active charts');
        if(scrollCalls!==0)fail('display-only visualization forced a page scroll');
        result.setAttribute('data-test-result','pass');
      },40);
    });
  });});
})();</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_FiltersHistoryAndFailuresBehaviorInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=agents');
  var result=document.getElementById('reconnect-result'),urls=[],failDashboard=false,destroyed=0;
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  function dashboard(){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},agents:[{agent_id:'agent-1',agent_name:'Agent One',tasks_evaluated:3,technical_completion:{percent:100},goal_achievement:{percent:100},first_pass:{percent:100},follow_up:{percent:0},duration_sample_size:3,median_duration_ms:1000}],agent_detail:null,workflows:[{workflow_id:'automation-1',workflow_name:'Automation One',invocation_count:1,completed_count:1,completion_rate:100}],recent_outcomes:[],insights:[]};}
  window.fetch=function(url){var value=String(url);urls.push(value);if(value.indexOf('/api/analytics/dashboard')>=0){if(failDashboard)return Promise.resolve({ok:false,status:500,json:function(){return Promise.resolve({});}});return Promise.resolve({ok:true,json:function(){return Promise.resolve(dashboard());}});}return Promise.resolve({ok:true,json:function(){return Promise.resolve([]);}});};
  window.Chart=function(){this.destroy=function(){destroyed++;};};
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out: '+urls.join('|'));setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){
    waitFor(function(){return document.querySelector('#analyticsAgentFilter option[value="agent-1"]');},function(){
      var agent=document.getElementById('analyticsAgentFilter');agent.value='agent-1';agent.dispatchEvent(new Event('change'));
      waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/dashboard')>=0&&url.indexOf('agent=agent-1')>=0;});},function(){
        history.pushState({},'',location.pathname+'?project_id=project-1&view=automations&agent=agent-1');window.dispatchEvent(new PopStateEvent('popstate'));
        waitFor(function(){return document.querySelector('#analyticsWorkflowFilter option[value="automation-1"]');},function(){
          var automation=document.getElementById('analyticsWorkflowFilter');automation.value='automation-1';automation.dispatchEvent(new Event('change'));
          waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/dashboard')>=0&&url.indexOf('workflow=automation-1')>=0&&url.indexOf('view=automations')>=0;});},function(){
            history.back();
            waitFor(function(){return document.getElementById('analyticsWorkflowFilter').value===''&&document.getElementById('analyticsAgentFilter').value==='agent-1';},function(){
              failDashboard=true;document.getElementById('refreshBtn').click();
              waitFor(function(){return !document.getElementById('analyticsError').classList.contains('hidden');},function(){if(destroyed<1)fail('active charts were not destroyed on failure');result.setAttribute('data-test-result','pass');});
            });
          });
        });
      });
    });
  });
})();</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_RejectsStaleDashboardAndEvidenceResponsesInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
	(function(){
	  history.replaceState({},'',location.pathname+'?project_id=project-1&view=outcomes');
		  var result=document.getElementById('reconnect-result'),staleResolve=null,staleSignal=null,agentBaseResolve=null,agentPageResolve=null,prematureAgentPageCalls=0;  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}  function dashboard(agent,title,total){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{},tasks_evaluated:agent?2:1},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[{agent_id:'agent-2',agent_name:'Agent Two',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:total,evidence_limit:20,evidence_offset:0,recent_outcomes:[{task_id:title,task_title:title,technical_result:'running',goal_result:'',merge_state:'',agent_id:agent,agent_name:agent?'Agent Two':'Unassigned',model:'Model',model_config_ids:[],terminal_period_statuses:[],category:'active',started_in_period:true,cycle_eligible:false,execution_count:1,period_completed_count:0,period_failed_count:0,period_cancelled_count:0,follow_up_count:0}],insights:[]};}
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

func TestBrowserFunctional_AnalyticsContent_ClearsChartsOnDimensionFilterChangeInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=outcomes');
  var result=document.getElementById('reconnect-result'),destroyed=[],urls=[];
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  function dashboard(){return {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[{label:'< 1m',count:1}],follow_up_distribution:[],agents:[{agent_id:'agent-1',agent_name:'Agent One',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],agent_skill_outcomes:[],skill_outcomes:[],model_categories:[],workflows:[],evidence_total:0,evidence_limit:20,evidence_offset:0,recent_outcomes:[],insights:[]};}
  window.Chart=function(ctx){var id=ctx&&ctx.canvas?ctx.canvas.id:'';this.destroy=function(){destroyed.push(id);};};
  window.fetch=function(url){var value=String(url),payload=[];urls.push(value);if(value.indexOf('/api/analytics/dashboard')>=0)payload=dashboard();return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out; urls='+urls.join('|'));setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){waitFor(function(){return window._analyticsCharts.cycleDistribution&&document.querySelector('#analyticsAgentFilter option[value="agent-1"]');},function(){
    var agent=document.getElementById('analyticsAgentFilter');agent.value='agent-1';agent.dispatchEvent(new Event('change'));
    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/dashboard')>=0&&new URL(url,location.href).searchParams.get('agent')==='agent-1';});},function(){
      if(destroyed.indexOf('cycleDistributionChart')<0)fail('dimension filter change left the old chart active');
      result.setAttribute('data-test-result','pass');
    });
  });});
})();</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_ExplicitSupportingLinksPreserveExactSubsetInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=learning&agent=agent-1');
  var result=document.getElementById('reconnect-result'),urls=[],longHandle='shared:review-skill-with-a-very-long-identical-handle';
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  window.Chart=function(){this.destroy=function(){};};
  window.fetch=function(url){var value=String(url),query=new URL(value,location.href).searchParams,payload=[];urls.push(value);
    if(value.indexOf('/api/analytics/dashboard')>=0)payload={definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[{agent_id:'agent-1',agent_name:'Agent One',technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}}],agent_detail:{agent_id:'agent-1',outcome_trend:[],categories:[],model_mix:[],failures:[],skills:[{skill_handle:longHandle,skill_scope:'agent_owned',tasks_evaluated:1,technical_completion:{},goal_achievement:{},follow_up:{}}],recent_tasks:[]},agent_skill_outcomes:[{agent_id:'agent-1',agent_name:'Agent One',skill_handle:longHandle,skill_scope:'project',tasks_evaluated:1,technical_completion:{},goal_achievement:{},follow_up:{}}],skill_outcomes:[{skill_handle:longHandle,skill_scope:'global',tasks_evaluated:1,technical_completion:{},goal_achievement:{},follow_up:{}}],model_categories:[],workflows:[],evidence_total:1,evidence_limit:20,evidence_offset:0,recent_outcomes:[],insights:[]};
    if(value.indexOf('/api/analytics/dashboard')>=0&&query.get('evidence_skill_handle'))payload.recent_outcomes=[{task_id:'skill-outcome-task',task_title:'Scoped skill outcome retry',technical_result:'failed',goal_result:'achieved',merge_state:'merged',agent_id:'agent-1',agent_name:'Agent One',model:'Model',model_config_ids:[],execution_hours:[10],terminal_period_statuses:[],category:'completed',started_in_period:true,goal_achievement_eligible:false,goal_achieved_in_period:false,cycle_eligible:false,execution_count:2,period_completed_count:1,period_failed_count:1,period_cancelled_count:0,follow_up_count:1}];
    if(value.indexOf('/api/analytics/skills')>=0)payload={usage_over_time:[],top_skills:[],follow_through:[],agent_usage:{agents:[],cells:[]},underused:[],evidence:[]};
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function waitFor(check,next,attempt){if(check()){next();return;}if((attempt||0)>100)fail('timed out; urls='+urls.join('|'));setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20);}
  window.addEventListener('load',function(){
    waitFor(function(){return document.querySelector('#skillOutcomeTable a[data-skill-outcome-evidence]')&&document.querySelector('#agentSkillOutcomeTable a[data-skill-outcome-evidence]');},function(){
      document.querySelector('[data-analytics-view="agents"]').click();
      waitFor(function(){return document.querySelector('#agentSkillTable a[data-skill-outcome-evidence]');},function(){
        var outcomeLink=document.querySelector('#skillOutcomeTable a[data-skill-outcome-evidence]');
        var pairLink=document.querySelector('#agentSkillOutcomeTable a[data-skill-outcome-evidence]');
        var agentLink=document.querySelector('#agentSkillTable a[data-skill-outcome-evidence]');
        var outcomeParams=new URL(outcomeLink.href).searchParams,pairParams=new URL(pairLink.href).searchParams,agentParams=new URL(agentLink.href).searchParams;
        if(outcomeParams.get('evidence_skill_handle')!==longHandle||outcomeParams.get('evidence_skill_scope')!=='global'||outcomeParams.get('evidence')!=='skill_outcomes')fail('skill outcome link lost scoped identity');
        if(pairParams.get('evidence_skill_handle')!==longHandle||pairParams.get('evidence_skill_scope')!=='project'||pairParams.get('evidence_skill_agent')!=='agent-1')fail('Agent-skill outcome link lost scoped identity');
        if(agentParams.get('evidence_skill_scope')!=='agent_owned'||agentParams.get('evidence_skill_agent')!=='agent-1')fail('selected Agent skill link lost scoped identity');
        pairLink.click();
        waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/dashboard')>=0&&url.indexOf('evidence_skill_handle='+encodeURIComponent(longHandle))>=0&&url.indexOf('evidence_skill_scope=project')>=0&&url.indexOf('evidence_skill_agent=agent-1')>=0;})&&document.getElementById('outcomeEvidenceTable').textContent.indexOf('Scoped skill outcome retry')>=0;},function(){
          var evidence=document.getElementById('outcomeEvidenceTable').textContent,params=new URLSearchParams(location.search);
          if(params.get('view')!=='outcomes'||params.get('evidence')!=='skill_outcomes'||evidence.indexOf('Completed (numerator; 1 completed, 1 failed, 0 cancelled)')<0||evidence.indexOf('Excluded from goal denominator')<0)fail('explicit scoped link did not render exact evidence');
          result.setAttribute('data-test-result','pass');
        });
      });
    });
  });
})();</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_LoadsOnlyVisibleViewDataInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><script>
(function() {
  var result = document.getElementById('reconnect-result'), urls = [], renderedCharts = [];
  function fail(message) { result.setAttribute('data-test-result', 'fail'); result.setAttribute('data-test-error', message); throw new Error(message); }
  history.replaceState({}, '', location.pathname + '?project_id=project-1&view=overview');
  window.Chart = function(ctx) { if (ctx && ctx.canvas) renderedCharts.push(ctx.canvas.id); this.destroy = function() {}; };
  window.fetch = function(url) {
    var value = String(url); urls.push(value);
    var payload = [];
    if (value.indexOf('/api/analytics/dashboard') >= 0) payload = {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},funnel:[],cycle_distribution:[],follow_up_distribution:[],agents:[],skill_outcomes:[{skill_handle:'project:visible-on-learning',skill_scope:'project',tasks_evaluated:1,technical_completion:{},goal_achievement:{},follow_up:{}}],agent_skill_outcomes:[],model_categories:[],workflows:[],recent_outcomes:[],insights:[]};
    if (value.indexOf('/api/analytics/usage') >= 0) payload = {usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[{provider:'openai',subscription_label:'ChatGPT Pro',limits:[]},{provider:'anthropic',subscription_label:'Claude Max',limits:[]}]};
    if (value.indexOf('/api/analytics/skills') >= 0) payload = {usage_over_time:[],top_skills:[],follow_through:[],agent_usage:{agents:[],cells:[]},underused:[],evidence:[]};
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function waitFor(check, next, attempt) { if (check()) { next(); return; } if ((attempt || 0) > 100) fail('timed out; urls=' + urls.join('|')); setTimeout(function(){waitFor(check,next,(attempt||0)+1);},20); }
  window.addEventListener('load', function() {
    waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/dashboard') >= 0;});}, function() {
      setTimeout(function() {
        var dashboardURL = urls.find(function(url){return url.indexOf('/api/analytics/dashboard') >= 0;});
        if (!dashboardURL || new URL(dashboardURL, location.href).searchParams.get('view') !== 'overview') fail('dashboard request did not preserve the visible view: ' + urls.join('|'));
        if (urls.some(function(url){return url.indexOf('/api/analytics/usage') >= 0 || url.indexOf('/api/analytics/skills') >= 0 || url.indexOf('/api/analytics/success-failure-rates') >= 0;})) fail('overview eagerly loaded unrelated hidden-view analytics: ' + urls.join('|'));
        if (renderedCharts.length || document.getElementById('agentPerformanceTable').innerHTML || document.getElementById('skillOutcomeTable').innerHTML || document.getElementById('workflowPerformanceTable').innerHTML || document.getElementById('modelCategoryTable').innerHTML) fail('overview synchronously rendered hidden-view analytics');
        document.querySelector('[data-analytics-view="usage"]').click();
        waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/usage') >= 0;}) && document.getElementById('accountUsageCards').textContent.indexOf('OpenAI') >= 0 && document.getElementById('accountUsageCards').textContent.indexOf('Anthropic') >= 0;}, function() {
          if (localStorage.getItem('openvibely.analytics.lastView.project-1') !== 'usage') fail('selected analytics tab was not remembered');
          document.querySelector('[data-analytics-view="learning"]').click();
          waitFor(function(){return urls.some(function(url){return url.indexOf('/api/analytics/skills') >= 0;}) && document.getElementById('skillOutcomeTable').textContent.indexOf('project:visible-on-learning') >= 0;}, function() {
            if (urls.some(function(url){return url.indexOf('/api/analytics/success-failure-rates') >= 0;})) fail('learning loaded unrelated analytics: ' + urls.join('|'));
            if (!document.getElementById('analytics-usage').classList.contains('hidden')) fail('usage remained visible outside the Usage tab');
            result.setAttribute('data-test-result', 'pass');
          });
        });
      }, 50);
    });
  });
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_RestoresLastViewInChrome(t *testing.T) {
	project := &models.Project{ID: "project-remember-view", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><script>
(function() {
  var result = document.getElementById('reconnect-result'), urls = [];
  function fail(message) { result.setAttribute('data-test-result', 'fail'); result.setAttribute('data-test-error', message); throw new Error(message); }
  history.replaceState({}, '', location.pathname + '?project_id=project-remember-view');
  localStorage.setItem('openvibely.analytics.lastView.project-remember-view', 'usage');
  window.Chart = function() { this.destroy = function() {}; };
  window.fetch = function(url) {
    var value = String(url); urls.push(value);
    var payload = [];
    if (value.indexOf('/api/analytics/dashboard') >= 0) payload = {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},model_categories:[],recent_outcomes:[],insights:[]};
    if (value.indexOf('/api/analytics/usage') >= 0) payload = {usage_rate:[],usage_rate_by_model:[],totals:{},model_breakdown:[],account_limits:[]};
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function wait(attempt) {
    var usageButton = document.querySelector('[data-analytics-view="usage"]');
    var dashboardURL = urls.find(function(url){return url.indexOf('/api/analytics/dashboard') >= 0;});
    if (usageButton && usageButton.classList.contains('btn-primary') && dashboardURL && urls.some(function(url){return url.indexOf('/api/analytics/usage') >= 0;})) {
      if (new URL(dashboardURL, location.href).searchParams.get('view') !== 'usage') fail('restored view was not sent to the dashboard endpoint: ' + dashboardURL);
      if (document.getElementById('analytics-usage').classList.contains('hidden')) fail('remembered Usage view was not shown');
      result.setAttribute('data-test-result', 'pass');
      return;
    }
    if ((attempt || 0) > 100) fail('remembered Usage view did not load; urls=' + urls.join('|'));
    setTimeout(function(){wait((attempt || 0) + 1);}, 20);
  }
  window.addEventListener('load', function(){wait(0);});
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_WorkflowPerformanceRendersInvocationStatusCountsInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><script>
(function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { result.setAttribute('data-test-result', 'fail'); result.setAttribute('data-test-error', message); throw new Error(message); }
  history.replaceState({}, '', location.pathname + '?project_id=project-1&view=automations');
  window.Chart = function() { this.destroy = function() {}; };
  window.fetch = function(url) {
    var value = String(url);
    var payload = [];
    if (value.indexOf('/api/analytics/dashboard') >= 0) payload = {definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},workflows:[{workflow_id:'workflow-1',workflow_name:'Workflow One',invocation_count:2,completed_count:1,failed_count:0,cancelled_count:1,skipped_count:0,open_count:0,completion_rate:50,average_duration_ms:60000,duration_sample_size:2,waiting_count:3,blocked_count:4,health:'degraded'}],recent_outcomes:[],insights:[]};
    return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});
  };
  function wait(attempt) {
    var text = document.getElementById('workflowPerformanceTable').textContent;
    if (text.indexOf('Workflow One') >= 0) {
      if (text.indexOf('1 / 2 (50.0%)') < 0) fail('workflow completion did not use invocation denominator: ' + text);
      if (text.indexOf('1 / 1 (100.0%)') >= 0) fail('workflow completion regressed to successful-terminal denominator: ' + text);
      var cells = Array.prototype.map.call(document.querySelectorAll('#workflowPerformanceTable td'), function(cell) { return cell.textContent.trim(); });
      if (cells.join('|') !== 'Workflow One|2|1 / 2 (50.0%)|0|1|0|0|1m 0s · n=2|3 / 4|degraded') fail('workflow status cells missing cancelled/skipped/open accounting: ' + cells.join('|'));
      result.setAttribute('data-test-result', 'pass');
      return;
    }
    if ((attempt || 0) > 100) fail('workflow row did not render');
    setTimeout(function() { wait((attempt || 0) + 1); }, 20);
  }
  window.addEventListener('load', function() { wait(0); });
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_ImmediateNavigationAwayAbortsWorkInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}

	fixture := `<main id="reconnect-result"></main><a id="other-nav" href="/tasks" data-nav-base="/tasks">Tasks</a><script>
(function() {
  var result = document.getElementById('reconnect-result'), requests = 0, requestSignals = [];
  function fail(message) { result.setAttribute('data-test-result', 'fail'); result.setAttribute('data-test-error', message); throw new Error(message); }
  history.replaceState({}, '', '/analytics?project_id=project-1&view=overview');
  window.Chart = function() { this.destroy = function() {}; };
  window.fetch = function(url, options) {
    requests++;
    requestSignals.push(options && options.signal);
    return new Promise(function() {});
  };
  function waitForRequest(attempt) {
	if (requestSignals.length === 1 && requestSignals.every(Boolean)) {
      var nav = document.getElementById('other-nav');
      nav.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true}));
      if (requestSignals.some(function(signal) { return !signal.aborted; })) fail('Analytics requests were not aborted before sidebar navigation');
      setTimeout(function() {
		if (requests !== 1) fail('Analytics started more work after navigation: ' + requests + ' requests');
        result.setAttribute('data-test-result', 'pass');
      }, 50);
      return;
    }
    if ((attempt || 0) > 100) fail('dashboard request did not start');
    setTimeout(function() { waitForRequest((attempt || 0) + 1); }, 20);
  }
  window.addEventListener('load', function() { waitForRequest(0); });
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_DirectFilteredURLAppliesFirstRequestsInChrome(t *testing.T) {
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
	  function wait(){var required=['/api/analytics/dashboard'];var ready=required.every(function(path){return urls.some(function(url){return url.indexOf(path)>=0&&url.indexOf('agent=agent-1')>=0&&url.indexOf('workflow=workflow-1')>=0;});});if(ready&&document.getElementById('analyticsAgentFilter').value==='agent-1'&&document.getElementById('analyticsWorkflowFilter').value==='workflow-1'){if(urls.some(function(url){return url.indexOf('/api/analytics/usage')>=0||url.indexOf('/api/analytics/skills')>=0||url.indexOf('success-failure-rates')>=0||url.indexOf('avg-execution-time-by-agent')>=0||url.indexOf('agent-usage-by-project')>=0;})){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error','agents view loaded unrelated analytics');return;}result.setAttribute('data-test-result','pass');return;}setTimeout(wait,20);}  window.addEventListener('load',wait);
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_ModelScorecardIsReadableWithoutHoverInChrome(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var rendered bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	fixture := `<main id="reconnect-result"></main><script>
(function(){
  history.replaceState({},'',location.pathname+'?project_id=project-1&view=models&range=30d');
  var result=document.getElementById('reconnect-result');
  function fail(message){result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',message);throw new Error(message);}
  window.Chart=function(){this.destroy=function(){};};
  window.fetch=function(url){var payload=[];if(String(url).indexOf('/api/analytics/dashboard')>=0)payload={definitions:[],current:{technical_completion:{},goal_achievement:{},first_pass:{},follow_up:{}},agents:[],workflows:[],model_categories:[],models:[{model_config_id:'model-1',config_name:'Fable',provider:'anthropic',model:'claude-fable',reasoning_effort:'high',tasks_used:1,run_count:2,average_runs:2,technical_completion:{numerator:2,denominator:2,percent:100},goal_achievement:{numerator:1,denominator:1,percent:100},first_pass:{numerator:0,denominator:1,percent:0},follow_up:{numerator:1,denominator:1,percent:100},median_duration_ms:60000,duration_sample_size:1,total_tokens:1000,token_covered_tasks:1,known_cost_usd:0.25,cost_covered_tasks:1}],recent_outcomes:[],insights:[]};return Promise.resolve({ok:true,json:function(){return Promise.resolve(payload);}});};
  function wait(attempt){var score=document.getElementById('modelScorecard').textContent,cost=document.getElementById('modelCostRanking').textContent,efficiency=document.getElementById('modelEfficiencyRanking').textContent;if(score.indexOf('Fable')>=0){if(score.indexOf('100.0%')<0||score.indexOf('1 / 1 · n=1')<0)fail('scorecard hides exact outcome evidence: '+score);if(cost.indexOf('$0.2500 / task')<0||cost.indexOf('cost n=1')<0)fail('cost ranking is unclear: '+cost);if(efficiency.indexOf('2.00 runs / task')<0||efficiency.indexOf('1m 0s median duration · n=1')<0)fail('efficiency ranking is unclear: '+efficiency);result.setAttribute('data-test-result','pass');return;}if((attempt||0)>100)fail('model scorecard did not render');setTimeout(function(){wait((attempt||0)+1);},20);}
  window.addEventListener('load',function(){wait(0);});
})();
</script>` + rendered.String()
	runReconnectChromeFixture(t, fixture)
}

func TestBrowserFunctional_AnalyticsContent_DelayedChartLoaderInitializesNewestGenerationOnceInChrome(t *testing.T) {
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
		`data-analytics-view="agents"`,
		`data-analytics-view="learning"`, `data-analytics-view="usage"`,
		`data-model-work-type-filter`, `id="analyticsWorkType"`, `Interactive tasks`, `Recurring work`,
		`data-analytics-section="overview"`, `data-analytics-section="outcomes"`,
		`data-analytics-section="agents"`, `data-analytics-section="automations"`,
		`Run success rate`, `Goal achievement rate`, `First-run success rate`,
		`Follow-up run rate`, `Median task duration`, `Cost per achieved goal`,
		`Outcome funnel`, `Supporting task evidence`, `Agent outcome comparison`, `Automation comparison`, `id="outcomeReadout"`, `id="usageFindings"`,
		`Observed skill outcomes`, `Exact skill outcome values`, `Provider Account Limits`,
		`Selected Agent outcome trend`, `Agent findings`, `Model outcome scorecard`, `Cost and token efficiency`,
		`Visual node funnel`, `Duration by node`, `Failures by node`, `Current bottlenecks`, `Model success by task category`,
		`Run results over time`, `Memory effectiveness is unavailable`, `id="loadMoreEvidence"`, `loaded ' + recent.length + ' of '`, `row.cycle_eligible ? formatDuration`, `row.duration_sample_size`, `focusUsageEvidence`, `id="skillEvidenceSelection"`, `id="usageEvidenceSelection"`, `loadSkillEvidence()`, `showUsageModelEvidence`, `history.replaceState`, `history.pushState`, `params.set('view'`, `params.set('agent'`, `params.set('workflow'`, `params.set('work_type'`, `params.set('evidence', key)`, `window.addEventListener('popstate'`, `renderChartState`, `destroyChart`, `escapeHTML(task.TaskTitle`, `canvas.setAttribute('aria-label'`,
	} {
		if !strings.Contains(content, expected) {
			t.Errorf("analytics outcome dashboard missing %q", expected)
		}
	}
	if strings.Contains(content, `data-analytics-view="all"`) || strings.Contains(content, `>All Metrics</button>`) {
		t.Fatal("Analytics should not expose the All Metrics view")
	}
	if strings.Contains(content, `onClick:`) || strings.Contains(content, `data-analytics-evidence-link`) {
		t.Fatal("Analytics charts, KPI cards, and funnel visuals must be display-only")
	}
	if strings.Contains(content, `id="overviewAccountUsageCards"`) || strings.Contains(content, `data-analytics-provider-usage`) {
		t.Fatal("provider usage should live only on the Usage view")
	}
	if !strings.Contains(content, `openvibely.analytics.lastView.`) || !strings.Contains(content, `savedAnalyticsView()`) {
		t.Fatal("Analytics should remember the selected view across navigation")
	}
	if !strings.Contains(content, `return 'Unavailable · n=0'`) || !strings.Contains(content, `usagePeriodForGroup`) {
		t.Fatal("Analytics should distinguish unavailable ratios and align cost with grouped outcome periods")
	}
	if strings.Contains(content, `label:'Sample size'`) || strings.Contains(content, `Definition and denominator`) {
		t.Fatal("KPI cards should not imply one universal sample or repeat definition tooltips")
	}
	for _, expected := range []string{`Current: n=`, `Compared with previous:`, `finished runs`, `goal-bearing tasks`, `tasks with runs`} {
		if !strings.Contains(content, expected) {
			t.Errorf("KPI comparison context missing %q", expected)
		}
	}
	if strings.Contains(content, `Low sample`) {
		t.Fatal("Analytics should rely on visible n values instead of low-sample badges")
	}
	if strings.Contains(content, `sticky top-0`) {
		t.Fatal("Analytics navigation should scroll with the page")
	}
	if !strings.Contains(content, `slice(0,12)`) || !strings.Contains(content, `canvas.parentElement.style.height`) || !strings.Contains(content, `legend:{display:false}`) {
		t.Fatal("Skill charts should bound dense data, scale horizontal rows, and avoid a per-skill legend")
	}
	if strings.Contains(content, `>${task.TaskTitle || 'Unknown'}<`) || strings.Contains(content, `<td>${pattern.TaskTitle || 'Unknown'}</td>`) {
		t.Fatal("dynamic task titles must be escaped before innerHTML insertion")
	}
}

func TestAnalyticsContent_CanonicalViewsOwnVisualizationsAndHideEvidence(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	content := buf.String()

	views := []string{"usage", "models", "learning", "agents"}
	if got := strings.Count(content, `data-analytics-view=`); got != len(views) {
		t.Fatalf("Analytics view count = %d, want exactly %d", got, len(views))
	}
	for _, view := range views {
		if !strings.Contains(content, `data-analytics-view="`+view+`"`) || !strings.Contains(content, `data-analytics-section="`+view+`"`) {
			t.Errorf("Analytics missing canonical %q view or section", view)
		}
	}
	previous := -1
	for _, view := range views {
		at := strings.Index(content, `data-analytics-view="`+view+`"`)
		if at <= previous {
			t.Fatalf("Analytics navigation order is wrong at %q", view)
		}
		previous = at
	}
	for _, forbidden := range []string{`data-analytics-view="all"`, `data-analytics-view="overview"`, `data-analytics-view="outcomes"`, `data-analytics-view="automations"`, `data-analytics-section="workflows"`, `viewDetailElements`} {
		if strings.Contains(content, forbidden) {
			t.Errorf("Analytics retains removed navigation/ownership construct %q", forbidden)
		}
	}

	section := func(name, next string) string {
		start := strings.Index(content, `data-analytics-section="`+name+`"`)
		if start < 0 {
			t.Fatalf("missing %s section", name)
		}
		end := len(content)
		if next != "" {
			end = strings.Index(content[start:], `data-analytics-section="`+next+`"`)
			if end < 0 {
				t.Fatalf("missing %s section after %s", next, name)
			}
			end += start
		}
		return content[start:end]
	}
	for name, expected := range map[string][]string{
		"overview":    {`id="projectOutcomeTrendChart"`, `id="overviewOutcomeFunnel"`, `Actionable exceptions`},
		"outcomes":    {`id="successFailureChart"`, `id="hourlyTrendsChart"`, `id="avgTimeTaskChart"`, `id="frequentTasksList"`, `id="failedPatternsChart"`, `id="failedPatternsTable"`},
		"agents":      {`id="agentComparisonChart"`, `id="agentEfficiencyChart"`, `id="agentCategoryChart"`},
		"models":      {`id="modelScorecard"`, `id="modelCostRanking"`, `id="modelEfficiencyRanking"`, `id="modelEfficiencyChart"`, `id="modelPerformanceTable"`},
		"automations": {`id="automationComparisonChart"`, `id="automationFunnelChart"`, `id="automationDurationChart"`, `id="automationFailureChart"`, `id="automationBottleneckChart"`},
		"learning":    {`id="skillUsageTrendChart"`, `id="skillTopChart"`, `id="skillFollowChart"`, `id="skillAgentChart"`, `id="underusedSkillsTable"`, `id="skillOutcomeChart"`, `id="skillEffectivenessChart"`},
		"usage":       {`id="accountUsageCards"`, `id="usageRateChart"`, `id="modelTokenBreakdownChart"`, `id="usageBreakdownTable"`, `id="costOutcomeTrendChart"`},
	} {
		next := map[string]string{"overview": "outcomes", "outcomes": "agents", "agents": "models", "models": "automations", "automations": "learning", "learning": "usage"}[name]
		body := section(name, next)
		for _, marker := range expected {
			if !strings.Contains(body, marker) {
				t.Errorf("%s does not physically own %q", name, marker)
			}
		}
	}
	for _, removed := range []string{`id="modelOutcomeChart"`, `id="modelCostQualityChart"`, `id="modelAttemptsChart"`} {
		if strings.Contains(content, removed) {
			t.Errorf("Models view should not render clipped scatter or dense comparison chart %q", removed)
		}
	}
	if strings.Contains(section("overview", "outcomes"), `id="recentOutcomesTable"`) || strings.Contains(section("overview", "outcomes"), `Recent outcomes`) {
		t.Fatal("Overview must not render a generic recent-outcomes table")
	}
	for _, id := range []string{"outcomeEvidenceTable", "agentEvidenceTable", "skillEventEvidenceTable", "usageEventEvidenceTable"} {
		at := strings.Index(content, `id="`+id+`"`)
		if at < 0 {
			t.Errorf("missing evidence region %s", id)
			continue
		}
		before := content[:at]
		open := strings.LastIndex(before, `<details`)
		close := strings.LastIndex(before, `</details>`)
		if open < 0 || close > open {
			t.Errorf("evidence region %s is not hidden in a disclosure", id)
		}
	}
}

func TestAnalyticsContent_FocusedViewsPreserveExistingMetrics(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project One"}
	var buf bytes.Buffer
	if err := AnalyticsContent(project).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render analytics content: %v", err)
	}
	content := buf.String()
	for _, metric := range []string{
		"Token Usage", "Model Breakdown by Tokens", "Token Usage Breakdown",
		"Runs by hour", "Average run time by task", "Model outcome scorecard",
		"Retry work and speed", "Most Frequently Run Tasks", "Skill Activity Over Time",
		"Top Skills", "Follow-through / Selected Outcomes", "Top Agent/Skill Pairs",
		"Least Active Enabled Skills", "Observed skill outcomes", "Model success by task category", "Failed Task Patterns", "accountUsageCards",
	} {
		if !strings.Contains(content, metric) {
			t.Errorf("Focused Analytics views lost existing metric %q", metric)
		}
	}
}
