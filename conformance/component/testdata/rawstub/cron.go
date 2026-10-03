package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schedule is P14.6: a five-field cron expression evaluated in a zone, or @every <duration>
// (at least 1 s) whose slots are the multiples of the duration since the Unix epoch.
type schedule struct {
	every  time.Duration
	fields [5]map[int]bool // minute, hour, day of month, month, day of week
}

var fieldRanges = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

func parseSchedule(s string) (*schedule, error) {
	if rest, ok := strings.CutPrefix(s, "@every "); ok {
		d, err := time.ParseDuration(rest)
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("@every needs a Go duration of at least 1s")
		}
		return &schedule{every: d}, nil
	}
	parts := strings.Fields(s)
	if len(parts) != 5 || strings.HasPrefix(s, "@") {
		return nil, fmt.Errorf("not a five-field cron expression or @every")
	}
	var sc schedule
	for i, p := range parts {
		set, err := parseField(p, fieldRanges[i][0], fieldRanges[i][1])
		if err != nil {
			return nil, fmt.Errorf("field %d: %v", i+1, err)
		}
		sc.fields[i] = set
	}
	return &sc, nil
}

func parseField(f string, lo, hi int) (map[int]bool, error) {
	set := map[int]bool{}
	for _, item := range strings.Split(f, ",") {
		rng, stepS, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad step %q", item)
			}
			step = n
		}
		from, to := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			x, err := strconv.Atoi(a)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", item)
			}
			from, to = x, x
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return nil, fmt.Errorf("bad range %q", item)
				}
			} else if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return nil, fmt.Errorf("%q out of %d-%d", item, lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// last is the most recent slot at or before now (P14.6: after downtime only that one runs).
func (s *schedule) last(now time.Time, zone *time.Location) time.Time {
	if s.every > 0 {
		return time.Unix(0, now.UnixNano()-now.UnixNano()%int64(s.every)).UTC()
	}
	t := now.In(zone).Truncate(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		if s.fields[0][t.Minute()] && s.fields[1][t.Hour()] && s.fields[2][t.Day()] && s.fields[3][int(t.Month())] && s.fields[4][int(t.Weekday())] {
			return t.UTC()
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}
}
