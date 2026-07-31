package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Entry struct {
	Rank        int
	Plugin      string
	Determinism string
	Ratio       float64
	Events      int
	Errors      int
	Eliminated  string
	Removed     int
	Baseline    int

	Description string
	Tier        string
	Version     string
	Domain      string
	Tags        []string
}

type catalogFile struct {
	Plugins []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Version     string   `json:"version"`
		Tier        string   `json:"tier"`
		Domain      string   `json:"domain"`
		Tags        []string `json:"tags"`
		Benchmark   string   `json:"benchmark"`
	} `json:"plugins"`
}

type Board struct {
	Entries      []Entry
	Unranked     []Entry
	TotalRemoved int
	TotalCalls   int
	TotalEvents  int
	TotalErrors  int
	Raw          string
}

func loadBoard(benchPath, catalogPath string) (Board, error) {
	var board Board

	raw, err := os.ReadFile(benchPath)
	if err != nil {
		return board, fmt.Errorf("bench output: %w", err)
	}
	board.Raw = strings.TrimRight(string(raw), "\n")

	var cat catalogFile
	if data, err := os.ReadFile(catalogPath); err == nil {
		if err := json.Unmarshal(data, &cat); err != nil {
			return board, fmt.Errorf("catalog index: %w", err)
		}
	}

	byName := map[string]int{}
	for i, p := range cat.Plugins {
		byName[p.Name] = i
	}

	ranked := map[string]bool{}
	for _, line := range strings.Split(board.Raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		rank, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		entry := Entry{
			Rank:        rank,
			Plugin:      fields[1],
			Determinism: fields[2],
			Eliminated:  strings.Join(fields[5:], " "),
		}
		entry.Ratio = percent(fields[2])
		entry.Events, _ = strconv.Atoi(fields[3])
		entry.Errors, _ = strconv.Atoi(fields[4])
		entry.Removed, entry.Baseline = eliminated(entry.Eliminated)

		if i, ok := byName[entry.Plugin]; ok {
			p := cat.Plugins[i]
			entry.Description, entry.Tier = p.Description, p.Tier
			entry.Version, entry.Domain, entry.Tags = p.Version, p.Domain, p.Tags
		}

		ranked[entry.Plugin] = true
		board.Entries = append(board.Entries, entry)
		board.TotalRemoved += entry.Removed
		board.TotalCalls += entry.Baseline
		board.TotalEvents += entry.Events
		board.TotalErrors += entry.Errors
	}

	for _, p := range cat.Plugins {
		if ranked[p.Name] {
			continue
		}
		board.Unranked = append(board.Unranked, Entry{
			Plugin: p.Name, Description: p.Description, Tier: p.Tier,
			Version: p.Version, Domain: p.Domain, Tags: p.Tags,
		})
	}

	return board, nil
}

func percent(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
	if err != nil {
		return 0
	}
	return v
}

func eliminated(s string) (removed, baseline int) {
	fields := strings.Fields(s)
	if len(fields) < 3 || fields[1] != "of" {
		return 0, 0
	}
	removed, _ = strconv.Atoi(fields[0])
	baseline, _ = strconv.Atoi(fields[2])
	return removed, baseline
}
