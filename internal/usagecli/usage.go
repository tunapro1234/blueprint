package usagecli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

const HistoryPath = "/srv/server-main/usage/history.jsonl"

type Resets struct {
	Five string `json:"5h"`
	Week string `json:"7d"`
}

type Sample struct {
	TS           string `json:"ts"`
	Claude5      any    `json:"claude_5h"`
	Claude7      any    `json:"claude_7d"`
	ClaudeFable7 any    `json:"claude_fable_7d"`
	ClaudeResets Resets `json:"claude_resets"`
	Codex5       any    `json:"codex_5h"`
	Codex7       any    `json:"codex_7d"`
	CodexResets  Resets `json:"codex_resets"`
}

func Latest(path string) (Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return Sample{}, err
	}
	defer file.Close()
	var last []byte
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			last = append(last[:0], scanner.Bytes()...)
		}
	}
	if err := scanner.Err(); err != nil {
		return Sample{}, err
	}
	if len(last) == 0 {
		return Sample{}, fmt.Errorf("history is empty")
	}
	var sample Sample
	err = json.Unmarshal(last, &sample)
	return sample, err
}

func Lines(sample Sample) []string {
	return []string{
		"ts: " + sample.TS,
		fmt.Sprintf("Claude: 5h %%%v (reset %s), 7d %%%v (reset %s), Fable 7d %%%v", sample.Claude5, sample.ClaudeResets.Five, sample.Claude7, sample.ClaudeResets.Week, sample.ClaudeFable7),
		fmt.Sprintf("Codex:  5h %%%v (reset %s), 7d %%%v (reset %s)", sample.Codex5, sample.CodexResets.Five, sample.Codex7, sample.CodexResets.Week),
	}
}
