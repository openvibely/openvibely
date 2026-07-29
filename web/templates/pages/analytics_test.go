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
		`afterEvent: function(chart, args)`,
		`chart.getElementsAtEventForMode(event, 'nearest', { intersect: false }, false)`,
		`afterDraw: function(chart)`,
		`chart.$analyticsHoveredPoint`,
		`position: 'nearest'`,
		`caretPadding: 6`,
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("Analytics line-chart hover marker should paint after the tooltip; missing %q", expected)
		}
	}
	if got := strings.Count(content, `plugins: [analyticsActivePointOnTop]`); got != 3 {
		t.Fatalf("expected all 3 Analytics line charts to use the hover layering plugin, got %d", got)
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
    var operations = [];
    var points = [
      {datasetIndex: 0, index: 0, element: {x: 40, y: 80, options: {backgroundColor: 'rgb(34, 197, 94)'}}},
      {datasetIndex: 1, index: 0, element: {x: 40, y: 20, options: {backgroundColor: 'rgb(59, 130, 246)'}}}
    ];
    var chart = {
      $analyticsHoveredPoint: null,
      chartArea: {left: 0, top: 0, right: 100, bottom: 100},
      tooltip: {opacity: 1},
      getElementsAtEventForMode: function(event, mode, options, useFinalPosition) {
        if (mode !== 'nearest' || options.intersect !== false || useFinalPosition !== false) fail('plugin did not request one pointer-nearest point');
        return event.y < 50 ? [points[1]] : [points[0]];
      },
      ctx: {
        save: function() { operations.push(['save']); },
        restore: function() { operations.push(['restore']); },
        beginPath: function() { operations.push(['beginPath']); },
        rect: function() { operations.push(['rect']); },
        clip: function() { operations.push(['clip']); },
        arc: function(x, y, radius) { operations.push(['arc', x, y, radius]); },
        fill: function() { operations.push(['fill', this.fillStyle]); },
        set fillStyle(value) { this._fillStyle = value; },
        get fillStyle() { return this._fillStyle; }
      }
    };
    var hoverArgs = {event: {type: 'mousemove', x: 40, y: 22}, inChartArea: true, changed: false};
    if (typeof plugin.afterEvent !== 'function') fail('plugin does not track the pointer-nearest point');
    plugin.afterEvent(chart, hoverArgs);
    if (chart.$analyticsHoveredPoint !== points[1]) fail('plugin selected the wrong series point under index interaction');
    if (!hoverArgs.changed) fail('plugin did not schedule a redraw when the nearest series point changed');
    plugin.afterDraw(chart);
    var arcs = operations.filter(function(operation) { return operation[0] === 'arc'; });
    var fills = operations.filter(function(operation) { return operation[0] === 'fill'; });
    if (arcs.length !== 2 || arcs[0][1] !== 40 || arcs[0][2] !== 20 || arcs[0][3] <= arcs[1][3]) fail('plugin did not paint a larger halo and inner marker at the selected point');
    if (fills.length !== 2 || fills[0][1] === fills[1][1] || fills[1][1] !== 'rgb(59, 130, 246)') fail('plugin marker lacks a contrasting halo or dataset-colored center');
    var outArgs = {event: {type: 'mouseout'}, inChartArea: false, changed: false};
    plugin.afterEvent(chart, outArgs);
    if (chart.$analyticsHoveredPoint !== null || !outArgs.changed) fail('plugin did not clear and redraw the marker on mouseout');
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
