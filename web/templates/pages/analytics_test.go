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
