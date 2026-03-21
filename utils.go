package main

import (
	"fmt"
	"math"
	"time"
)

const (
	bytesPerKB = 1024
	bytesPerMB = 1024 * 1024
	bytesPerGB = 1024 * 1024 * 1024
	bytesPerTB = 1024 * 1024 * 1024 * 1024
)

// FormatBytes converts bytes to a human-readable string.
func FormatBytes(b float64) string {
	abs := math.Abs(b)
	switch {
	case abs >= bytesPerTB:
		return fmt.Sprintf("%.2f TB", b/bytesPerTB)
	case abs >= bytesPerGB:
		return fmt.Sprintf("%.2f GB", b/bytesPerGB)
	case abs >= bytesPerMB:
		return fmt.Sprintf("%.2f MB", b/bytesPerMB)
	case abs >= bytesPerKB:
		return fmt.Sprintf("%.2f KB", b/bytesPerKB)
	default:
		return fmt.Sprintf("%.0f B", b)
	}
}

// NowInZone returns current time in the given IANA timezone.
// Falls back to system local time if tz is empty or invalid.
func NowInZone(tz string) time.Time {
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return time.Now().In(loc)
		}
	}
	return time.Now()
}

// GetCycleDates returns the start and end dates of the current billing cycle.
// Handles months with fewer days than resetDay by clamping to last day of month.
func GetCycleDates(resetDay int, tz ...string) (start, end time.Time) {
	tzStr := ""
	if len(tz) > 0 {
		tzStr = tz[0]
	}
	now := NowInZone(tzStr)
	year, month, day := now.Date()
	loc := now.Location()

	// lastDay returns the last day of a given month
	lastDay := func(y int, m time.Month) int {
		return time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	}

	// Clamp resetDay to actual days in a given month
	clamp := func(y int, m time.Month, d int) int {
		last := lastDay(y, m)
		if d > last {
			return last
		}
		return d
	}

	thisMonthReset := clamp(year, month, resetDay)

	if day >= thisMonthReset {
		// Current cycle started this month
		start = time.Date(year, month, thisMonthReset, 0, 0, 0, 0, loc)
		// End = day before next month's (clamped) reset day
		nextStart := time.Date(year, month+1, clamp(year, month+1, resetDay), 0, 0, 0, 0, loc)
		end = nextStart.AddDate(0, 0, -1)
		end = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, loc)
	} else {
		// Current cycle started last month
		start = time.Date(year, month-1, clamp(year, month-1, resetDay), 0, 0, 0, 0, loc)
		// End = day before this month's reset day
		end = time.Date(year, month, thisMonthReset-1, 23, 59, 59, 0, loc)
		if thisMonthReset <= 1 {
			// resetDay=1 and we're somehow before day 1 — shouldn't happen, but safety
			end = start
		}
	}
	return
}

// GetPrevCycleStart returns the start date of the previous billing cycle (for data cleanup).
func GetPrevCycleStart(resetDay int, tz ...string) time.Time {
	start, _ := GetCycleDates(resetDay, tz...)
	return start.AddDate(0, -1, 0)
}

// DaysUntilReset returns how many days until the next reset.
func DaysUntilReset(resetDay int, tz ...string) int {
	tzStr := ""
	if len(tz) > 0 {
		tzStr = tz[0]
	}
	_, end := GetCycleDates(resetDay, tzStr)
	now := NowInZone(tzStr)
	days := int(end.Sub(now).Hours()/24) + 1
	if days < 0 {
		days = 0
	}
	return days
}

// ProgressBar generates a text-based progress bar.
func ProgressBar(percent float64, width int) string {
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}
	filled := int(percent / 100 * float64(width))
	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	return bar
}
