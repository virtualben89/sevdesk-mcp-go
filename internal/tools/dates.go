package tools

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // scratch images have no zoneinfo
)

var berlin = func() *time.Location {
	l, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		return time.UTC
	}
	return l
}()

// parseDay accepts YYYY-MM-DD, DD.MM.YYYY, RFC 3339 or a unix timestamp (seconds).
func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && len(s) >= 9 {
		return time.Unix(n, 0).In(berlin), nil
	}
	for _, layout := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, s, berlin); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(berlin), nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q (use YYYY-MM-DD)", s)
}

// unixStart converts a date to the unix timestamp (seconds) of its start in Europe/Berlin.
// sevDesk list filters startDate/endDate are unix timestamps.
func unixStart(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, err := parseDay(s)
	if err != nil {
		return "", err
	}
	y, m, d := t.Date()
	return strconv.FormatInt(time.Date(y, m, d, 0, 0, 0, 0, berlin).Unix(), 10), nil
}

// unixEnd converts a date to the last second of that day in Europe/Berlin.
func unixEnd(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, err := parseDay(s)
	if err != nil {
		return "", err
	}
	y, m, d := t.Date()
	return strconv.FormatInt(time.Date(y, m, d, 23, 59, 59, 0, berlin).Unix(), 10), nil
}

// germanDate formats a date as DD.MM.YYYY, which sevDesk accepts in request bodies.
func germanDate(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, err := parseDay(s)
	if err != nil {
		return "", err
	}
	return t.Format("02.01.2006"), nil
}

// unixInt converts a date into a unix timestamp number (some booking endpoints want an integer).
func unixInt(s string) (int64, error) {
	u, err := unixStart(s)
	if err != nil || u == "" {
		return 0, err
	}
	return strconv.ParseInt(u, 10, 64)
}
