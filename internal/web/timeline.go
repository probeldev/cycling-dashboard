package web

import (
	"fmt"
	"net/url"
	"time"

	"cycling-dashboard/internal/store"
)

const (
	timelineDefaultDays = 180
	timelineMaxDays     = 3660 // ~10 лет: больше в один ответ не отдаём
)

// parseTimelineRange разбирает ?from/?to (YYYY-MM-DD, локальные даты).
// По умолчанию — последние 180 дней до сегодня включительно.
func parseTimelineRange(q url.Values, now time.Time) (from, to time.Time, err error) {
	to = store.StartOfDay(now)
	if v := q.Get("to"); v != "" {
		to, err = time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return from, to, fmt.Errorf("to=%q: нужно YYYY-MM-DD", v)
		}
	}
	from = to.AddDate(0, 0, -(timelineDefaultDays - 1))
	if v := q.Get("from"); v != "" {
		from, err = time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return from, to, fmt.Errorf("from=%q: нужно YYYY-MM-DD", v)
		}
	}
	if from.After(to) {
		return from, to, fmt.Errorf("from позже to")
	}
	if days := int(to.Sub(from)/(24*time.Hour)) + 1; days > timelineMaxDays {
		return from, to, fmt.Errorf("диапазон %d дней, максимум %d", days, timelineMaxDays)
	}
	return from, to, nil
}
