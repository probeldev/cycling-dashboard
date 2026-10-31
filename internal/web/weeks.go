package web

import (
	"fmt"
	"net/url"
	"strconv"
)

const (
	defaultWeeks = 12
	maxWeeks     = 52
)

// parseWeeksN разбирает ?n для /api/weeks: по умолчанию 12, максимум 52.
func parseWeeksN(q url.Values) (int, error) {
	if v := q.Get("n"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("n=%q: нужно целое число", v)
		}
		if n < 1 {
			return 0, fmt.Errorf("n=%d: нужно не меньше 1", n)
		}
		if n > maxWeeks {
			return 0, fmt.Errorf("n=%d: не больше %d", n, maxWeeks)
		}
		return n, nil
	}
	return defaultWeeks, nil
}
