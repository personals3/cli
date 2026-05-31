package commands

import (
	"fmt"
	"os"
	"time"
)

// humanizeUntil renders an absolute time as "<local clock> (in <relative>)".
// Format adapts to how far in the future the time is:
//
//	< 24h     → "10:24:08 (in 2m)"
//	< 7d      → "Mon 10:24 (in 3d 4h)"
//	otherwise → "2026-06-07 10:24 (in 8d)"
//
// Past times render as "(expired 2m ago)".
func humanizeUntil(t time.Time) string {
	now := time.Now()
	d := time.Until(t)
	if d < 0 {
		return fmt.Sprintf("(expired %s ago)", humanizeDuration(-d))
	}
	var clock string
	switch {
	case d < 24*time.Hour && t.Day() == now.Day():
		clock = t.Local().Format("15:04:05")
	case d < 7*24*time.Hour:
		clock = t.Local().Format("Mon 15:04")
	default:
		clock = t.Local().Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("%s (in %s)", clock, humanizeDuration(d))
}

// humanizeDuration renders a duration with at most two units, e.g.
// "3d 4h", "1h 12m", "45s". Anything under 60s is just "<N>s".
func humanizeDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// formatBytes renders byte counts as humans expect — "1.2 MB", not 1234567.
// Negative or unknown sizes render as "?".
func formatBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// stderrCounter writes informational lines to stderr so stdout stays clean
// for piping (e.g. `ps3 ls | grep ...`).
func stderrCounter() *os.File { return os.Stderr }
