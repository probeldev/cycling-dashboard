package web

import (
	"fmt"
	"net/url"
	"time"

	"cycling-dashboard/internal/store"
)

// parseRecordsPeriod разбирает ?period (30d|90d|365d|all) в диапазон [from, to)
// и длину предыдущего периода в днях (0 — предыдущего нет). По умолчанию all.
// Верхняя граница — now, а не конец сегодняшнего дня: будущая тренировка не
// должна попадать в рекорды и CP-фит.
func parseRecordsPeriod(q url.Values, now time.Time) (from, to time.Time, prevDays int, err error) {
	to = now
	switch q.Get("period") {
	case "", "all":
		from = time.Time{}
	case "30d":
		from = store.StartOfDay(now).AddDate(0, 0, -29)
		prevDays = 30
	case "90d":
		from = store.StartOfDay(now).AddDate(0, 0, -89)
		prevDays = 90
	case "365d":
		from = store.StartOfDay(now).AddDate(0, 0, -364)
		prevDays = 365
	default:
		return from, to, 0, fmt.Errorf("period=%q: ожидается 30d, 90d, 365d или all", q.Get("period"))
	}
	return from, to, prevDays, nil
}

// parseIncludeTests разбирает ?includeTests: по умолчанию тестовые тренировки
// исключены из рекордов, CP-фита и PR-флагов; includeTests=1 возвращает их.
func parseIncludeTests(q url.Values) (bool, error) {
	switch q.Get("includeTests") {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, fmt.Errorf("includeTests=%q: ожидается 0 или 1", q.Get("includeTests"))
	}
}
