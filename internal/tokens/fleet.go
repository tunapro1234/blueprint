package tokens

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FleetRow is one agent+model's token totals over a range, with the subagent/
// workflow portion split out. It is the unit that travels between machines for
// `bp tokens --fleet`: small, model-level (so it can be priced), and free of
// any machine-specific or prompt context. The totals already include Sub*.
type FleetRow struct {
	Agent      string `json:"agent"`
	Src        string `json:"src"`
	Model      string `json:"model,omitempty"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
	// Sub* is the portion of the totals above that came from subagents and
	// workflows (Kind != ""), attributed to this main agent.
	SubRequests   int64 `json:"sub_reqs,omitempty"`
	SubIn         int64 `json:"sub_in,omitempty"`
	SubOut        int64 `json:"sub_out,omitempty"`
	SubCacheWrite int64 `json:"sub_cache_write,omitempty"`
	SubCacheRead  int64 `json:"sub_cache_read,omitempty"`
}

// FleetSummary aggregates the daily rollups over period into per-(agent, src,
// model) rows, always keeping the model so the result can be priced. Unlike
// Summary it never collapses the model dimension, which is what lets a fleet
// view compute an API-equivalent cost per model. It reads only the durable
// daily tier, so it is cheap and stays correct after raw files age out.
func FleetSummary(root string, period Range) ([]FleetRow, error) {
	type key struct{ agent, src, model string }
	grouped := map[key]*FleetRow{}
	for year := period.Start.Year(); year <= period.End.Year(); year++ {
		var rows []DailyRecord
		path := filepath.Join(root, "daily", time.Date(year, 1, 1, 0, 0, 0, 0, istanbul).Format("2006")+".jsonl")
		if err := readJSONLines(path, &rows); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, row := range rows {
			if !period.contains(row.Day) {
				continue
			}
			k := key{row.Agent, row.Src, row.Model}
			target := grouped[k]
			if target == nil {
				target = &FleetRow{Agent: row.Agent, Src: row.Src, Model: row.Model}
				grouped[k] = target
			}
			target.Requests += row.Requests
			target.In += row.In
			target.Out += row.Out
			target.CacheWrite += row.CacheWrite
			target.CacheRead += row.CacheRead
			if row.Kind != "" {
				target.SubRequests += row.Requests
				target.SubIn += row.In
				target.SubOut += row.Out
				target.SubCacheWrite += row.CacheWrite
				target.SubCacheRead += row.CacheRead
			}
		}
	}
	out := make([]FleetRow, 0, len(grouped))
	for _, row := range grouped {
		out = append(out, *row)
	}
	sortFleetRows(out)
	return out, nil
}

func sortFleetRows(rows []FleetRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Out != rows[j].Out {
			return rows[i].Out > rows[j].Out
		}
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
		}
		if rows[i].Src != rows[j].Src {
			return rows[i].Src < rows[j].Src
		}
		return rows[i].Model < rows[j].Model
	})
}

// Claude API list prices, USD per 1,000,000 tokens, by model family. These
// mirror the monitor's and console's price table ("if it were API"); a non-
// Claude model (e.g. a Codex/GPT model) prices to zero so it still contributes
// token counts but no dollar figure.
const (
	cacheReadMultiplier  = 0.1
	cacheWriteMultiplier = 1.25
)

// ClaudeModelRate returns the per-1M input and output USD price for a model
// name, matching the families used elsewhere. ok is false for a non-Claude
// model, which then contributes no API-equivalent cost.
func ClaudeModelRate(model string) (inputPerM, outputPerM float64, ok bool) {
	switch m := strings.ToLower(model); {
	case strings.Contains(m, "opus"):
		return 5, 25, true
	case strings.Contains(m, "sonnet"):
		return 3, 15, true
	case strings.Contains(m, "haiku"):
		return 1, 5, true
	case strings.Contains(m, "fable"):
		return 10, 50, true
	default:
		return 0, 0, false
	}
}

// RowCostUSD prices one row's tokens at Claude API rates (input, output, and
// cache reads/writes by Anthropic's multipliers). A non-Claude model costs 0.
func RowCostUSD(row FleetRow) float64 {
	in, out, ok := ClaudeModelRate(row.Model)
	if !ok {
		return 0
	}
	const perM = 1_000_000.0
	cost := float64(row.In)/perM*in + float64(row.Out)/perM*out
	cost += float64(row.CacheRead) / perM * in * cacheReadMultiplier
	cost += float64(row.CacheWrite) / perM * in * cacheWriteMultiplier
	return cost
}
