package repository

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"math"
	"sort"
	"time"
)

// GetUsagePage computes only the displayed local metrics, reading events once.
func (r *UsageRepo) GetUsagePage(ctx context.Context, filter UsageFilter) (*models.AnalyticsUsageViewModel, error) {
	view := &models.AnalyticsUsageViewModel{UsageRate: []models.UsageRatePoint{}, UsageRateByModel: []models.UsageRatePoint{}, ModelBreakdown: []models.ModelUsagePoint{}}
	rates := map[usageModelKey]*models.UsageRatePoint{}
	breakdown := map[usageModelKey]*models.ModelUsagePoint{}
	configurations := map[[3]string]*models.ConfigurationUsagePoint{}
	view.ConfigurationBreakdown = []models.ConfigurationUsagePoint{}
	group := normalizedUsageGroupBy(filter.GroupBy)
	var firstEvent time.Time
	err := r.forEachUsageAggregateEvent(ctx, filter, func(e usageAggregateEvent) {
		if firstEvent.IsZero() || e.OccurredAt.Before(firstEvent) {
			firstEvent = e.OccurredAt
		}
		t := &view.Totals
		t.InputTokens += e.InputTokens
		t.OutputTokens += e.OutputTokens
		t.CachedInputTokens += e.CacheTokens
		t.CacheCreationInputTokens += e.CacheCreationTokens
		t.CacheReadInputTokens += e.CacheReadTokens
		t.ReasoningOutputTokens += e.ReasoningOutputTokens
		t.TotalTokens += e.TotalTokens
		t.CallCount++
		if e.CostValid {
			if t.CostUSD == nil {
				t.CostUSD = new(float64)
			}
			*t.CostUSD += e.CostUSD
			t.CostAvailable = true
		}
		if view.LastUpdatedAt == nil || e.OccurredAt.After(*view.LastUpdatedAt) {
			date := e.OccurredAt
			view.LastUpdatedAt = &date
		}
		period := usageLocalPeriod(e.OccurredAt, group)
		configKey := [3]string{e.AgentConfigID, e.Provider, e.Model}
		c := configurations[configKey]
		if c == nil {
			c = &models.ConfigurationUsagePoint{ModelConfigID: e.AgentConfigID, ModelUsagePoint: models.ModelUsagePoint{Provider: e.Provider, Model: e.Model}}
			configurations[configKey] = c
		}
		c.InputTokens += e.InputTokens
		c.OutputTokens += e.OutputTokens
		c.CacheTokens += e.CacheTokens
		c.ReasoningOutputTokens += e.ReasoningOutputTokens
		c.TotalTokens += e.TotalTokens
		c.CallCount++
		if e.CostValid {
			if c.CostUSD == nil {
				c.CostUSD = new(float64)
			}
			*c.CostUSD += e.CostUSD
		}
		key := usageModelKey{Provider: e.Provider, Model: e.Model}
		m := breakdown[key]
		if m == nil {
			m = &models.ModelUsagePoint{Provider: e.Provider, Model: e.Model}
			breakdown[key] = m
		}
		m.InputTokens += e.InputTokens
		m.OutputTokens += e.OutputTokens
		m.CacheTokens += e.CacheTokens
		m.ReasoningOutputTokens += e.ReasoningOutputTokens
		m.TotalTokens += e.TotalTokens
		m.CallCount++
		if e.CostValid {
			if m.CostUSD == nil {
				m.CostUSD = new(float64)
			}
			*m.CostUSD += e.CostUSD
		}
		key.Period = period
		p := rates[key]
		if p == nil {
			p = &models.UsageRatePoint{Period: period, Provider: e.Provider, Model: e.Model}
			rates[key] = p
		}
		p.TotalTokens += e.TotalTokens
		p.CallCount++
	})
	if err != nil {
		return nil, err
	}
	combined := map[string]*models.UsageRatePoint{}
	from, to := filter.DateFrom, filter.DateTo
	if from.IsZero() {
		from = firstEvent
		if !from.IsZero() {
			d := from.In(time.Local)
			from = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
		}
	}
	if to.IsZero() {
		d := time.Now().In(time.Local)
		to = time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, time.Local)
	}
	view.UsageCalendarDays = usageCalendarDays(from, to)
	if view.UsageCalendarDays > 0 {
		average := float64(view.Totals.TotalTokens) / float64(view.UsageCalendarDays)
		view.AverageTokensPerDay = &average
	}
	// Resolve saved configuration labels once, after the event cursor is closed.
	configRows, err := r.db.QueryContext(ctx, `SELECT id, name, COALESCE(reasoning_effort,'') FROM agent_configs`)
	if err != nil {
		return nil, err
	}
	labels := map[string][2]string{}
	for configRows.Next() {
		var id, name, effort string
		if err := configRows.Scan(&id, &name, &effort); err != nil {
			configRows.Close()
			return nil, err
		}
		labels[id] = [2]string{name, effort}
	}
	err = configRows.Err()
	configRows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range configurations {
		label := labels[c.ModelConfigID]
		c.ConfigName, c.ReasoningEffort = label[0], label[1]
		if view.Totals.TotalTokens > 0 {
			c.Percent = float64(c.TotalTokens) * 100 / float64(view.Totals.TotalTokens)
		}
		view.ConfigurationBreakdown = append(view.ConfigurationBreakdown, *c)
	}
	sort.Slice(view.ConfigurationBreakdown, func(i, j int) bool {
		a, b := view.ConfigurationBreakdown[i], view.ConfigurationBreakdown[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.ConfigName != b.ConfigName {
			return a.ConfigName < b.ConfigName
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.ModelConfigID < b.ModelConfigID
	})
	for _, key := range sortedUsageModelKeys(rates) {
		p := rates[key]
		view.UsageRateByModel = append(view.UsageRateByModel, *p)
		c := combined[p.Period]
		if c == nil {
			c = &models.UsageRatePoint{Period: p.Period}
			combined[p.Period] = c
		}
		c.TotalTokens += p.TotalTokens
		c.CallCount += p.CallCount
	}
	for _, p := range combined {
		view.UsageRate = append(view.UsageRate, *p)
	}
	sort.Slice(view.UsageRate, func(i, j int) bool { return view.UsageRate[i].Period < view.UsageRate[j].Period })
	for _, p := range breakdown {
		if view.Totals.TotalTokens > 0 {
			p.Percent = float64(p.TotalTokens) * 100 / float64(view.Totals.TotalTokens)
		}
		view.ModelBreakdown = append(view.ModelBreakdown, *p)
	}
	sort.Slice(view.ModelBreakdown, func(i, j int) bool {
		a, b := view.ModelBreakdown[i], view.ModelBreakdown[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model < b.Model
	})
	view.Evidence, view.EvidenceTotal, err = r.GetEvidence(ctx, filter)
	return view, err
}

// Measure local calendar-day spans, rounding partial days up and ignoring DST shifts.
func usageCalendarDays(from, to time.Time) int {
	if from.IsZero() || !to.After(from) {
		return 0
	}
	a, b := from.In(time.Local), to.In(time.Local)
	start := time.Date(a.Year(), a.Month(), a.Day(), a.Hour(), a.Minute(), a.Second(), a.Nanosecond(), time.UTC)
	end := time.Date(b.Year(), b.Month(), b.Day(), b.Hour(), b.Minute(), b.Second(), b.Nanosecond(), time.UTC)
	return int(math.Ceil(end.Sub(start).Hours() / 24))
}
