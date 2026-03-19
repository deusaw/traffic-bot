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

// GetCycleDates returns the start and end dates of the current billing cycle.
func GetCycleDates(resetDay int) (start, end time.Time) {
	now := time.Now()
	year, month, day := now.Date()

	if day >= resetDay {
		// Current cycle: this month's reset day -> next month's reset day - 1
		start = time.Date(year, month, resetDay, 0, 0, 0, 0, time.Local)
		end = time.Date(year, month+1, resetDay-1, 23, 59, 59, 0, time.Local)
	} else {
		// Current cycle: last month's reset day -> this month's reset day - 1
		start = time.Date(year, month-1, resetDay, 0, 0, 0, 0, time.Local)
		end = time.Date(year, month, resetDay-1, 23, 59, 59, 0, time.Local)
	}
	return
}

// GetPrevCycleStart returns the start date of the previous billing cycle (for data cleanup).
func GetPrevCycleStart(resetDay int) time.Time {
	start, _ := GetCycleDates(resetDay)
	// Previous cycle starts one month before current cycle start
	return start.AddDate(0, -1, 0)
}

// DaysUntilReset returns how many days until the next reset.
func DaysUntilReset(resetDay int) int {
	_, end := GetCycleDates(resetDay)
	now := time.Now()
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
