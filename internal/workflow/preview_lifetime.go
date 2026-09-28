package workflow

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

func NormalizePreviewTTL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	return value
}

// ParsePreviewTTL accepts zero for unlimited lifetime, whole days, or a Go
// duration. Subsecond deadlines are not useful for deployment cleanup.
func ParsePreviewTTL(value string) (time.Duration, error) {
	value = NormalizePreviewTTL(value)
	if value == "0" {
		return 0, nil
	}
	var duration time.Duration
	var err error
	if strings.HasSuffix(value, "d") {
		var days int64
		days, err = strconv.ParseInt(strings.TrimSuffix(value, "d"), 10, 64)
		if days > 0 && days <= math.MaxInt64/int64(24*time.Hour) {
			duration = time.Duration(days) * 24 * time.Hour
		}
	} else {
		duration, err = time.ParseDuration(value)
	}
	if err != nil || duration < time.Second || duration%time.Second != 0 {
		return 0, errors.New("preview ttl must be 0 (unlimited) or a positive duration such as 1d, 24h, or 30m")
	}
	return duration, nil
}
