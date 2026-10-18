package core

import "testing"

func TestScheduleFromSetting(t *testing.T) {
	for _, tc := range []struct {
		in   string
		secs int
		cron string
		bad  bool
	}{
		{in: "06:00", cron: "daily 06:00"},
		{in: " 7:30 ", cron: "daily 7:30"},
		{in: "MON,FRI 09:00", cron: "MON,FRI 09:00"},
		{in: "weekdays 08:15", cron: "weekdays 08:15"},
		{in: "30", secs: 1800},
		{in: "2", secs: MinAppScheduleSeconds},
		{in: "0", bad: true},
		{in: "", bad: true},
		{in: "breakfast", bad: true},
		{in: "25:00", bad: true},
	} {
		secs, cron, err := ScheduleFromSetting(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("%q should not read as a time, got %d %q", tc.in, secs, cron)
			}
			continue
		}
		if err != nil || secs != tc.secs || cron != tc.cron {
			t.Errorf("%q = %d %q %v, want %d %q", tc.in, secs, cron, err, tc.secs, tc.cron)
		}
	}
}
