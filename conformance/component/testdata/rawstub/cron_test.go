package main

import (
	"testing"
	"time"
)

func TestScheduleSlots(t *testing.T) {
	every, err := parseSchedule("@every 2s")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1759999999, 500_000_000)
	if got := every.last(now, time.UTC); got.Unix() != 1759999998 {
		t.Errorf("@every 2s last = %v", got.Unix())
	}
	daily, err := parseSchedule("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	got := daily.last(time.Date(2026, 10, 3, 2, 0, 0, 0, sh), sh)
	if want := time.Date(2026, 10, 2, 3, 0, 0, 0, sh); !got.Equal(want) {
		t.Errorf("daily last = %v, want %v", got, want)
	}
	for _, bad := range []string{"@daily", "* * * *", "@every 500ms", "61 * * * *", "* * * * * *"} {
		if _, err := parseSchedule(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
