package main

import (
	"testing"

	"github.com/sadatdaniel/download_me_not/internal/engine"
)

func TestParseRate(t *testing.T) {
	for in, want := range map[string]int64{"0": 0, "500K": 500 << 10, "5M": 5 << 20, "1.5G": 3 << 29, "2MB/s": 2 << 20, "1024": 1024} {
		if got, err := parseRate(in); err != nil || got != want {
			t.Errorf("%s: got %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "fast", "-1M"} {
		if _, err := parseRate(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPieceBar(t *testing.T) {
	runs := []engine.PieceRun{{Length: 4, Complete: true}, {Length: 2, Partial: true}, {Length: 2}}
	if got := pieceBar(runs, 8, 4); got != "██▒░" {
		t.Fatalf("got %q", got)
	}
	if got := pieceBar(runs, 8, 16); len([]rune(got)) != 16 {
		t.Fatalf("width: %q", got)
	}
}

func TestParseSeed(t *testing.T) {
	if p, err := parseSeed("ratio:1.5"); err != nil || p.Policy != "ratio" || p.Ratio != 1.5 {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := parseSeed("time:60"); err != nil || p.Minutes != 60 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"ratio:x", "sometimes", "time:"} {
		if _, err := parseSeed(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
