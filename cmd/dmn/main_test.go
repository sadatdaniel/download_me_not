package main

import "testing"

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
