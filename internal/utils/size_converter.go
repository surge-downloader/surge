package utils

import (
	"fmt"
	"math"
)

// ConvertBytesToHumanReadable converts a given number of bytes into a human-readable format (e.g., KB, MB, GB).
func ConvertBytesToHumanReadable(bytes int64) string {
	if bytes == 0 {
		return "0 B"
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	exp := int64(math.Log(float64(bytes)) / math.Log(unit))
	pre := "KMGTPE"[exp-1]
	return fmt.Sprintf("%.1f %cB", float64(bytes)/math.Pow(unit, float64(exp)), pre)
}
