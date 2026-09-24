package repository

import (
	"encoding/json"
	"sort"

	"github.com/openvibely/openvibely/internal/models"
)

// Samples come from the same task rows as the full-period comparison metrics.
func aggregateModelEffortTrend(raw string) ([]models.ModelEffortTrend, error) {
	var samples []struct {
		Period    string
		Duration  *int64
		Tokens    *int64
		Followups int
	}
	if err := json.Unmarshal([]byte(raw), &samples); err != nil {
		return nil, err
	}
	groups := map[string]*models.ModelEffortTrend{}
	durations := map[string][]int64{}
	for _, s := range samples {
		p := groups[s.Period]
		if p == nil {
			p = &models.ModelEffortTrend{Period: s.Period}
			groups[s.Period] = p
		}
		p.Tasks++
		p.FollowUps += s.Followups
		if s.Tokens != nil {
			p.Tokens += *s.Tokens
			p.TokenSamples++
		}
		if s.Duration != nil {
			durations[s.Period] = append(durations[s.Period], *s.Duration)
		}
	}
	result := make([]models.ModelEffortTrend, 0, len(groups))
	for period, p := range groups {
		d := durations[period]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		p.DurationSamples = len(d)
		if len(d) > 0 {
			p.MedianDurationMs = (d[(len(d)-1)/2] + d[len(d)/2]) / 2
		}
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Period < result[j].Period })
	return result, nil
}
