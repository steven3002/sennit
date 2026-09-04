package main

import (
	"fmt"
	"os"
	"time"
)

// stderr carries progress and diagnostics; stdout carries results, so a caller
// can pipe one without the other.
var stderr = os.Stderr

// took renders a duration at a precision that stays readable across the four
// orders of magnitude these operations span.
func took(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2f s", d.Seconds())
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
