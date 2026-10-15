package main

import (
	"fmt"
	"sort"
	"strings"

	"blueprint/internal/p2p"
	"blueprint/internal/tokens"
)

// machineCost holds one machine's rolled-up totals for the fleet view, priced
// at Claude API-equivalent rates. Rows keeps the per-(agent, model) detail so a
// breakdown can be printed under each machine.
type machineCost struct {
	Machine  string
	Error    string
	Requests int64
	In       int64
	Out      int64
	CacheW   int64
	CacheR   int64
	USD      float64
	Rows     []tokens.FleetRow
}

// tokensFleet queries every connected, opted-in peer (and this machine) for
// per-(agent, model) token totals over a trailing window, prices them at
// Claude API-equivalent rates, and prints a per-machine and combined view. A
// peer that is unreachable or has not granted token access is listed with its
// error rather than silently dropped.
func (a *app) tokensFleet(options tokenOptions) error {
	since := options.since
	if since <= 0 {
		since = 1
	}
	report, err := p2p.FleetTokens(a.ctx, a.config.StateDir, since)
	if err != nil {
		return fmt.Errorf("fleet tokens: %w", err)
	}
	machines := make([]machineCost, 0, len(report.Machines))
	for _, machine := range report.Machines {
		cost := machineCost{Machine: machine.Machine, Error: machine.Error}
		for _, row := range machine.Rows {
			fleetRow := tokens.FleetRow{
				Agent: row.Agent, Src: row.Src, Model: row.Model,
				Requests: row.Requests, In: row.In, Out: row.Out,
				CacheWrite: row.CacheWrite, CacheRead: row.CacheRead,
				SubRequests: row.SubRequests, SubIn: row.SubIn, SubOut: row.SubOut,
				SubCacheWrite: row.SubCacheWrite, SubCacheRead: row.SubCacheRead,
			}
			cost.Requests += row.Requests
			cost.In += row.In
			cost.Out += row.Out
			cost.CacheW += row.CacheWrite
			cost.CacheR += row.CacheRead
			cost.USD += tokens.RowCostUSD(fleetRow)
			cost.Rows = append(cost.Rows, fleetRow)
		}
		machines = append(machines, cost)
	}
	if options.json {
		return writeTokenJSON(a.out, fleetJSON(report.SinceDays, machines, options))
	}
	renderFleetTokens(a, report.SinceDays, machines, options)
	return nil
}

func fleetJSON(since int, machines []machineCost, options tokenOptions) any {
	type machineOut struct {
		Machine  string            `json:"machine"`
		Error    string            `json:"error,omitempty"`
		Requests int64             `json:"reqs"`
		In       int64             `json:"in"`
		Out      int64             `json:"out"`
		CacheW   int64             `json:"cache_write"`
		CacheR   int64             `json:"cache_read"`
		USD      float64           `json:"api_usd"`
		Rows     []tokens.FleetRow `json:"rows,omitempty"`
	}
	out := struct {
		SinceDays int          `json:"since_days"`
		Machines  []machineOut `json:"machines"`
		TotalUSD  float64      `json:"total_api_usd"`
		SeatUSD   float64      `json:"seat_usd,omitempty"`
		Seats     int          `json:"seats,omitempty"`
		SeatCost  float64      `json:"seat_cost,omitempty"`
		SeatRatio float64      `json:"seat_ratio,omitempty"`
	}{SinceDays: since}
	for _, m := range machines {
		out.Machines = append(out.Machines, machineOut{
			Machine: m.Machine, Error: m.Error, Requests: m.Requests,
			In: m.In, Out: m.Out, CacheW: m.CacheW, CacheR: m.CacheR,
			USD: m.USD, Rows: m.Rows,
		})
		out.TotalUSD += m.USD
	}
	if options.perSeatUSD > 0 {
		seats := options.seats
		if seats < 1 {
			seats = 1
		}
		out.SeatUSD = options.perSeatUSD
		out.Seats = seats
		out.SeatCost = options.perSeatUSD * float64(seats)
		if out.SeatCost > 0 {
			out.SeatRatio = out.TotalUSD / out.SeatCost
		}
	}
	return out
}

func renderFleetTokens(a *app, since int, machines []machineCost, options tokenOptions) {
	fmt.Fprintf(a.out, "=== bp tokens --fleet — last %dd (Europe/Istanbul) ===\n\n", since)
	fmt.Fprintf(a.out, "%-16s %7s %9s %9s %9s %9s %10s\n", "MACHINE", "REQS", "OUT", "IN", "CACHE-W", "CACHE-R", "API-USD")
	var total machineCost
	for _, m := range machines {
		label := m.Machine
		if label == "" {
			label = "(unknown)"
		}
		if m.Error != "" {
			fmt.Fprintf(a.out, "%-16s %s\n", truncateTokenText(label, 16), "("+m.Error+")")
			continue
		}
		fmt.Fprintf(a.out, "%-16s %7d %9s %9s %9s %9s %10s\n",
			truncateTokenText(label, 16), m.Requests, tokens.Human(m.Out), tokens.Human(m.In),
			tokens.Human(m.CacheW), tokens.Human(m.CacheR), fmt.Sprintf("$%.2f", m.USD))
		total.Requests += m.Requests
		total.In += m.In
		total.Out += m.Out
		total.CacheW += m.CacheW
		total.CacheR += m.CacheR
		total.USD += m.USD
	}
	fmt.Fprintf(a.out, "%-16s %7d %9s %9s %9s %9s %10s\n",
		"TOTAL", total.Requests, tokens.Human(total.Out), tokens.Human(total.In),
		tokens.Human(total.CacheW), tokens.Human(total.CacheR), fmt.Sprintf("$%.2f", total.USD))

	if options.perSeatUSD > 0 {
		seats := options.seats
		if seats < 1 {
			seats = 1
		}
		seatCost := options.perSeatUSD * float64(seats)
		fmt.Fprintf(a.out, "\nSubscription basis: %d seat(s) x $%.2f = $%.2f.\n", seats, options.perSeatUSD, seatCost)
		if seatCost > 0 {
			fmt.Fprintf(a.out, "API-equivalent spend $%.2f = %.1f%% of seat cost.\n", total.USD, total.USD/seatCost*100)
		}
	}

	// Per-machine agent/model breakdown, so a single machine's heavy agents are
	// visible rather than only its total. Rows arrive already sorted by output.
	for _, m := range machines {
		if m.Error != "" || len(m.Rows) == 0 {
			continue
		}
		fmt.Fprintf(a.out, "\n--- %s: top agents ---\n", m.Machine)
		fmt.Fprintf(a.out, "%-24s %-10s %7s %9s %10s\n", "AGENT", "MODEL", "REQS", "OUT", "API-USD")
		rows := append([]tokens.FleetRow(nil), m.Rows...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Out > rows[j].Out })
		limit := len(rows)
		if limit > options.top && options.top > 0 {
			limit = options.top
		}
		for _, row := range rows[:limit] {
			fmt.Fprintf(a.out, "%-24s %-10s %7d %9s %10s\n",
				truncateTokenText(row.Agent, 24), truncateTokenText(modelFamily(row.Model), 10),
				row.Requests, tokens.Human(row.Out), fmt.Sprintf("$%.2f", tokens.RowCostUSD(row)))
		}
		if limit < len(rows) {
			fmt.Fprintf(a.out, "(+%d more agent/model rows; raise --top to see them)\n", len(rows)-limit)
		}
	}
}

// modelFamily shortens a full model id to its family for the compact fleet
// breakdown (e.g. "claude-opus-4-8" -> "opus"); unknown names pass through.
func modelFamily(model string) string {
	lower := strings.ToLower(model)
	for _, family := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	if model == "" {
		return "-"
	}
	return model
}
