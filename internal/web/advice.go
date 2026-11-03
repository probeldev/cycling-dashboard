package web

import (
	"database/sql"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/store"
)

// workoutAdvice собирает контекст дня и зон для правил advice и применяет
// их. Контекст отдельно нужен деталке: из него же строится сводка Summary.
func workoutAdvice(db *sql.DB, w *store.Workout) ([]advice.Card, error) {
	ctx, err := workoutAdviceContext(db, w)
	if err != nil {
		return nil, err
	}
	return advice.For(*w, ctx), nil
}

// workoutAdviceContext собирает контекст дня и зон для правил advice. В пакет
// advice ничего из БД не передаётся: вчерашний TSS, TSB и полярность недели
// считаются здесь, decoupling — по сэмплам только при наличии пульса.
func workoutAdviceContext(db *sql.DB, w *store.Workout) (advice.Context, error) {
	zones, err := store.WorkoutZones(db, w.ID)
	if err != nil {
		return advice.Context{}, err
	}
	day := store.StartOfDay(w.StartTime)
	days, err := store.Timeline(db, day.AddDate(0, 0, -1), day)
	if err != nil {
		return advice.Context{}, err
	}
	var yesterday, tsb float64
	if len(days) == 2 {
		yesterday = days[0].TSS
		tsb = days[1].TSB
	}
	weeks, err := store.Weeks(db, 1, w.StartTime)
	if err != nil {
		return advice.Context{}, err
	}
	var gray float64
	if len(weeks) > 0 {
		gray = weeks[0].GrayPct
	}

	var decoupling *float64
	if w.AvgHR > 0 {
		samples, err := store.RawSamples(db, w.ID)
		if err != nil {
			return advice.Context{}, err
		}
		if v, ok := advice.Decoupling(samples); ok {
			decoupling = &v
		}
	}
	return advice.Context{
		Zones:        zones,
		YesterdayTSS: yesterday,
		TSB:          tsb,
		WeekGrayPct:  gray,
		Decoupling:   decoupling,
	}, nil
}
