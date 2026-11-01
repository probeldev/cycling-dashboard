package web

import (
	"database/sql"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/store"
)

// workoutAdvice собирает контекст дня и зон для правил advice. В пакет
// advice ничего из БД не передаётся: вчерашний TSS, TSB и полярность недели
// считаются здесь, decoupling — по сэмплам только при наличии пульса.
func workoutAdvice(db *sql.DB, w *store.Workout) ([]advice.Card, error) {
	zones, err := store.WorkoutZones(db, w.ID)
	if err != nil {
		return nil, err
	}
	day := store.StartOfDay(w.StartTime)
	days, err := store.Timeline(db, day.AddDate(0, 0, -1), day)
	if err != nil {
		return nil, err
	}
	var yesterday, tsb float64
	if len(days) == 2 {
		yesterday = days[0].TSS
		tsb = days[1].TSB
	}
	weeks, err := store.Weeks(db, 1, w.StartTime)
	if err != nil {
		return nil, err
	}
	var gray float64
	if len(weeks) > 0 {
		gray = weeks[0].GrayPct
	}

	var decoupling *float64
	if w.AvgHR > 0 {
		samples, err := store.RawSamples(db, w.ID)
		if err != nil {
			return nil, err
		}
		if v, ok := advice.Decoupling(samples); ok {
			decoupling = &v
		}
	}
	return advice.For(*w, advice.Context{
		Zones:        zones,
		YesterdayTSS: yesterday,
		TSB:          tsb,
		WeekGrayPct:  gray,
		Decoupling:   decoupling,
	}), nil
}
