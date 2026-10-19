// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"testing"
	"time"
)

func TestZurichSlotsAcrossDSTAndMissedDays(t *testing.T) {
	s := DefaultSettings()
	for _, tc := range []struct{ now, want string }{
		{"2026-03-28T09:00:00Z", "2026-03-28T08:00:00Z"},
		{"2026-03-29T08:00:00Z", "2026-03-29T07:00:00Z"},
		{"2026-03-29T06:59:00Z", "2026-03-28T08:00:00Z"},
		{"2026-10-24T08:00:00Z", "2026-10-24T07:00:00Z"},
		{"2026-10-25T09:00:00Z", "2026-10-25T08:00:00Z"},
		{"2026-11-04T17:00:00Z", "2026-11-04T08:00:00Z"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			got, err := LatestSlot(now, s)
			if err != nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("got %v %v, want %s", got, err, tc.want)
			}
		})
	}
	s.Timezone = "Invalid/Zone"
	if _, err := LatestSlot(time.Now(), s); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
func TestNativeNormalization(t *testing.T) {
	for native, want := range map[string]string{"DETECTED": "open", "CONFIRMED": "open", "fixed": "resolved", "auto_dismissed": "dismissed", "new-state": "unknown"} {
		if got := State(native); got != want {
			t.Errorf("%s => %s", native, got)
		}
	}
	for native, want := range map[string]string{"MODERATE": "medium", "informational": "info", "error": "unknown", "CRITICAL": "critical"} {
		if got := Severity(native); got != want {
			t.Errorf("%s => %s", native, got)
		}
	}
	first, second := NewID(), NewID()
	if first == second {
		t.Fatal("installation identifiers collided")
	}
}

func TestCustomSlotsAcrossSkippedAndRepeatedTimes(t *testing.T) {
	for _, tc := range []struct{ zone, now, want string }{
		{"Europe/Zurich", "2026-03-29T00:15:00Z", "2026-03-28T01:30:00Z"},
		{"Europe/Zurich", "2026-03-29T01:15:00Z", "2026-03-28T01:30:00Z"},
		{"Europe/Zurich", "2026-03-29T01:30:00Z", "2026-03-29T01:30:00Z"},
		{"Europe/Zurich", "2026-10-25T00:15:00Z", "2026-10-24T00:30:00Z"},
		{"Europe/Zurich", "2026-10-25T00:30:00Z", "2026-10-25T00:30:00Z"},
		{"Europe/Zurich", "2026-10-25T01:45:00Z", "2026-10-25T00:30:00Z"},
		{"America/New_York", "2026-03-08T06:45:00Z", "2026-03-07T07:30:00Z"},
		{"America/New_York", "2026-03-08T07:15:00Z", "2026-03-07T07:30:00Z"},
		{"America/New_York", "2026-03-08T07:30:00Z", "2026-03-08T07:30:00Z"},
	} {
		t.Run(tc.zone+"/"+tc.now, func(t *testing.T) {
			s := DefaultSettings()
			s.Timezone, s.Hour, s.Minute = tc.zone, 2, 30
			now, _ := time.Parse(time.RFC3339, tc.now)
			slot, err := LatestSlot(now, s)
			if err != nil || slot.Format(time.RFC3339) != tc.want {
				t.Fatalf("got %v %v, want %s", slot, err, tc.want)
			}
		})
	}
}
