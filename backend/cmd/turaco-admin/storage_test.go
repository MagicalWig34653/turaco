package main

import "testing"

func TestParseRewrapArgs(t *testing.T) {
	got, err := parseRewrapArgs(nil)
	if err != nil || got.apply || got.batch != 100 {
		t.Fatalf("default: %+v %v", got, err)
	}
	got, err = parseRewrapArgs([]string{"--apply", "--batch-size", "25"})
	if err != nil || !got.apply || got.batch != 25 {
		t.Fatalf("apply: %+v %v", got, err)
	}
	for _, args := range [][]string{{"--batch-size", "0"}, {"--batch-size", "1001"}, {"other"}} {
		if _, err := parseRewrapArgs(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
