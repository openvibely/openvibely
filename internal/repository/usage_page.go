package repository

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"sort"
)

// GetUsagePage computes only the displayed local metrics, reading events once.
func (r *UsageRepo) GetUsagePage(ctx context.Context, filter UsageFilter) (*models.AnalyticsUsageViewModel, error) {
	view := &models.AnalyticsUsageViewModel{UsageRate: []models.UsageRatePoint{}, UsageRateByModel: []models.UsageRatePoint{}, ModelBreakdown: []models.ModelUsagePoint{}}
	rates := map[usageModelKey]*models.UsageRatePoint{}
	breakdown := map[usageModelKey]*models.ModelUsagePoint{}
	group := normalizedUsageGroupBy(filter.GroupBy)
	err := r.forEachUsageAggregateEvent(ctx, filter, func(e usageAggregateEvent) {
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
