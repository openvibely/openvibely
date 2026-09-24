package repository

import "testing"

func TestAggregateModelEffortTrend(t *testing.T) {
	rows, err := aggregateModelEffortTrend(`[{"period":"b","duration":null,"tokens":null,"followups":0},{"period":"a","duration":0,"tokens":0,"followups":1},{"period":"a","duration":20,"tokens":100,"followups":2},{"period":"a","duration":100,"tokens":200,"followups":0}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Period != "a" || rows[0].MedianDurationMs != 20 || rows[0].Tasks != 3 || rows[0].FollowUps != 3 || rows[0].TokenSamples != 3 || rows[0].Tokens != 300 || rows[0].DurationSamples != 3 {
		t.Fatalf("bad aggregates: %+v", rows)
	}
	if rows[1].Tasks != 1 || rows[1].TokenSamples != 0 || rows[1].DurationSamples != 0 {
		t.Fatalf("missing samples became zeros: %+v", rows[1])
	}
}
