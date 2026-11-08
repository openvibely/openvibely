package pageplugins

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"math/rand"
	"time"
)

// SalesBuiltinName is the in-process backend used by the bundled Sales example.
const SalesBuiltinName = "sales-demo"

//go:embed examples/sales
var salesExample embed.FS

// SalesExampleFS returns the bundled Sales example package files.
func SalesExampleFS() fs.FS {
	sub, err := fs.Sub(salesExample, "examples/sales")
	if err != nil {
		panic(err)
	}
	return sub
}

type salesRow struct {
	Date    string  `json:"date"`
	Region  string  `json:"region"`
	Product string  `json:"product"`
	Units   int     `json:"units"`
	Revenue float64 `json:"revenue"`
}

type salesState struct {
	Revision    int        `json:"revision"`
	Target      float64    `json:"target"`
	GeneratedAt time.Time  `json:"generated_at"`
	Rows        []salesRow `json:"rows"`
}

type salesReport struct {
	Revision    int                `json:"revision"`
	Target      float64            `json:"target"`
	GeneratedAt time.Time          `json:"generated_at"`
	Region      string             `json:"region"`
	Rows        []salesRow         `json:"rows"`
	Totals      map[string]float64 `json:"totals"`
}

type salesBuiltin struct{}

var (
	salesRegions  = []string{"north", "south", "east", "west"}
	salesProducts = []string{"Starter", "Pro", "Enterprise"}
	salesPrices   = map[string]float64{"Starter": 49, "Pro": 199, "Enterprise": 899}
)

func generateSales(instanceID string, revision int, now time.Time) []salesRow {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s:%d", instanceID, revision)
	rng := rand.New(rand.NewSource(int64(h.Sum64())))
	rows := make([]salesRow, 0, 14*len(salesRegions))
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -13)
	for d := 0; d < 14; d++ {
		day := start.AddDate(0, 0, d).Format("2006-01-02")
		for _, region := range salesRegions {
			product := salesProducts[rng.Intn(len(salesProducts))]
			units := 1 + rng.Intn(40)
			rows = append(rows, salesRow{Date: day, Region: region, Product: product, Units: units, Revenue: float64(units) * salesPrices[product]})
		}
	}
	return rows
}

func (salesBuiltin) Invoke(_ context.Context, req BuiltinRequest) (BuiltinResult, error) {
	var st salesState
	_ = json.Unmarshal([]byte(nonEmptyJSON(req.Instance.StateJSON)), &st)
	var in struct {
		Region string  `json:"region"`
		Target float64 `json:"target"`
	}
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return BuiltinResult{}, err
		}
	}
	changed := false
	now := time.Now()
	if st.Revision == 0 {
		st = salesState{Revision: 1, Target: 250000, GeneratedAt: now, Rows: generateSales(req.Instance.ID, 1, now)}
	}
	switch req.Operation {
	case "refresh_data":
		st.Revision++
		st.GeneratedAt = now
		st.Rows = generateSales(req.Instance.ID, st.Revision, now)
		changed = true
	case "get_report":
	case "set_target":
		st.Target = in.Target
		changed = true
		out, _ := json.Marshal(map[string]float64{"target": st.Target})
		state, _ := json.Marshal(st)
		return BuiltinResult{Output: out, State: state}, nil
	default:
		return BuiltinResult{}, fmt.Errorf("unknown operation %q", req.Operation)
	}
	region := in.Region
	if region == "" {
		region = "all"
	}
	rep := salesReport{Revision: st.Revision, Target: st.Target, GeneratedAt: st.GeneratedAt, Region: region, Rows: []salesRow{}, Totals: map[string]float64{"revenue": 0, "units": 0}}
	for _, r := range st.Rows {
		if region != "all" && r.Region != region {
			continue
		}
		rep.Rows = append(rep.Rows, r)
		rep.Totals["revenue"] += r.Revenue
		rep.Totals["units"] += float64(r.Units)
	}
	out, err := json.Marshal(rep)
	if err != nil {
		return BuiltinResult{}, err
	}
	res := BuiltinResult{Output: out}
	if changed {
		res.State, _ = json.Marshal(st)
	}
	return res, nil
}
