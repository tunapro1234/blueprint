package tokens

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type Range struct {
	Start time.Time
	End   time.Time
}

func DayRange(day time.Time) Range {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, istanbul)
	return Range{Start: start, End: start}
}

func LastDaysRange(now time.Time, days int) Range {
	end := time.Date(now.In(istanbul).Year(), now.In(istanbul).Month(), now.In(istanbul).Day(), 0, 0, 0, 0, istanbul)
	return Range{Start: end.AddDate(0, 0, -(days - 1)), End: end}
}

func (r Range) Label() string {
	if r.Start.Format("2006-01-02") == r.End.Format("2006-01-02") {
		return r.Start.Format("2006-01-02")
	}
	return r.Start.Format("2006-01-02") + ".." + r.End.Format("2006-01-02")
}

func (r Range) contains(day string) bool {
	return day >= r.Start.Format("2006-01-02") && day <= r.End.Format("2006-01-02")
}

type SummaryRow struct {
	Agent      string `json:"agent"`
	Src        string `json:"src"`
	Model      string `json:"model,omitempty"`
	Requests   int64  `json:"reqs"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheWrite int64  `json:"cache_write"`
	CacheRead  int64  `json:"cache_read"`
}

type SummaryResult struct {
	Day  string       `json:"day"`
	Rows []SummaryRow `json:"rows"`
}

func Summary(root string, period Range, agent string) (SummaryResult, error) {
	type key struct {
		agent, src, model string
	}
	grouped := map[key]*SummaryRow{}
	for year := period.Start.Year(); year <= period.End.Year(); year++ {
		var rows []DailyRecord
		err := readJSONLines(filepath.Join(root, "daily", time.Date(year, 1, 1, 0, 0, 0, 0, istanbul).Format("2006")+".jsonl"), &rows)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return SummaryResult{}, err
		}
		for _, row := range rows {
			if !period.contains(row.Day) || (agent != "" && row.Agent != agent) {
				continue
			}
			model := ""
			if agent != "" {
				model = row.Model
			}
			k := key{row.Agent, row.Src, model}
			target := grouped[k]
			if target == nil {
				target = &SummaryRow{Agent: row.Agent, Src: row.Src, Model: model}
				grouped[k] = target
			}
			target.Requests += row.Requests
			target.In += row.In
			target.Out += row.Out
			target.CacheWrite += row.CacheWrite
			target.CacheRead += row.CacheRead
		}
	}
	result := SummaryResult{Day: period.Label(), Rows: make([]SummaryRow, 0, len(grouped))}
	for _, row := range grouped {
		result.Rows = append(result.Rows, *row)
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].Out == result.Rows[j].Out {
			if result.Rows[i].Agent == result.Rows[j].Agent {
				if result.Rows[i].Src == result.Rows[j].Src {
					return result.Rows[i].Model < result.Rows[j].Model
				}
				return result.Rows[i].Src < result.Rows[j].Src
			}
			return result.Rows[i].Agent < result.Rows[j].Agent
		}
		return result.Rows[i].Out > result.Rows[j].Out
	})
	return result, nil
}

type HourResult struct {
	Day  string         `json:"day"`
	Rows []HourlyRecord `json:"rows"`
}

func Hours(root string, period Range, agent string) (HourResult, error) {
	months := monthNames(period)
	var result HourResult
	result.Day = period.Label()
	for _, month := range months {
		var rows []HourlyRecord
		err := readJSONLines(filepath.Join(root, "hourly", month+".jsonl"), &rows)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return HourResult{}, err
		}
		for _, row := range rows {
			if period.contains(row.Day) && (agent == "" || row.Agent == agent) {
				result.Rows = append(result.Rows, row)
			}
		}
	}
	sortHourly(result.Rows)
	return result, nil
}

type PromptResult struct {
	Day  string         `json:"day"`
	Rows []PromptRecord `json:"rows"`
}

func Prompts(root string, period Range, agent string, top int) (PromptResult, error) {
	type key struct {
		agent, src, prompt string
	}
	grouped := map[key]*PromptRecord{}
	for day := period.Start; !day.After(period.End); day = day.AddDate(0, 0, 1) {
		dayText := day.Format("2006-01-02")
		var rows []PromptRecord
		path := filepath.Join(root, "prompts", dayText+".jsonl")
		err := readJSONLines(path, &rows)
		if errors.Is(err, os.ErrNotExist) {
			err = readGzipJSONLines(path+".gz", &rows)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return PromptResult{}, err
		}
		for _, row := range rows {
			if agent != "" && row.Agent != agent {
				continue
			}
			k := key{row.Agent, row.Src, row.PromptID}
			target := grouped[k]
			if target == nil {
				copy := row
				target = &copy
				grouped[k] = target
				continue
			}
			if row.FirstTS < target.FirstTS {
				target.FirstTS = row.FirstTS
			}
			if target.Model != row.Model {
				target.Model = "multiple"
			}
			target.Requests += row.Requests
			target.In += row.In
			target.Out += row.Out
			target.CacheWrite += row.CacheWrite
			target.CacheRead += row.CacheRead
		}
	}
	result := PromptResult{Day: period.Label(), Rows: make([]PromptRecord, 0, len(grouped))}
	for _, row := range grouped {
		result.Rows = append(result.Rows, *row)
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].Out == result.Rows[j].Out {
			return result.Rows[i].FirstTS < result.Rows[j].FirstTS
		}
		return result.Rows[i].Out > result.Rows[j].Out
	})
	if top > 0 && len(result.Rows) > top {
		result.Rows = result.Rows[:top]
	}
	return result, nil
}

func monthNames(period Range) []string {
	current := time.Date(period.Start.Year(), period.Start.Month(), 1, 0, 0, 0, 0, istanbul)
	end := time.Date(period.End.Year(), period.End.Month(), 1, 0, 0, 0, 0, istanbul)
	var names []string
	for !current.After(end) {
		names = append(names, current.Format("2006-01"))
		current = current.AddDate(0, 1, 0)
	}
	return names
}

func Human(value int64) string {
	if value >= 1_000_000 {
		return formatOneDecimal(value, 1_000_000) + "M"
	}
	if value >= 1_000 {
		return formatOneDecimal(value, 1_000) + "k"
	}
	return itoa(value)
}

func formatOneDecimal(value, divisor int64) string {
	whole := value / divisor
	tenth := (value % divisor) * 10 / divisor
	return itoa(whole) + "." + itoa(tenth)
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	var buffer [24]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
