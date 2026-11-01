package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

const (
	// weeklyTargetKey — ключ настройки недельной цели TSS.
	weeklyTargetKey = "weekly_tss_target"

	// WeeklyTargetMin и WeeklyTargetMax — допустимые границы ручной цели.
	// Ниже 100 TSS неделя не даёт тренировочного смысла, выше 1500 — уже
	// не цель, а опечатка.
	WeeklyTargetMin = 100.0
	WeeklyTargetMax = 1500.0

	// defaultWeeklyTarget — цель, когда истории нагрузки ещё нет.
	defaultWeeklyTarget = 150.0
)

// WeeklyTarget — цель недельного TSS. Приоритет: явная настройка в settings,
// затем медиана последних четырёх полных недель (пн–вс, без текущей), затем
// TSS последней полной недели, затем 150. Недели без тренировок участвуют
// нулями: пропуск — это тоже факт истории. Если медиана вышла нулевой
// (вся нагрузка только в текущей неделе), возвращаем дефолт, а не цель 0.
func WeeklyTarget(db DBTX, now time.Time) (float64, error) {
	if v, ok, err := weeklyTargetSetting(db); err != nil {
		return 0, err
	} else if ok {
		return v, nil
	}

	first, ok, err := firstWorkoutDay(db)
	if err != nil {
		return 0, err
	}
	if !ok {
		return defaultWeeklyTarget, nil
	}

	lastFull := WeekStart(now).AddDate(0, 0, -7)
	firstWeek := WeekStart(first)
	if firstWeek.After(lastFull) {
		return defaultWeeklyTarget, nil
	}
	weeks := int(lastFull.Sub(firstWeek).Hours()/24/7) + 1
	if weeks > 4 {
		weeks = 4
	}

	if weeks < 4 {
		tss, err := weeklyTSS(db, lastFull)
		if err != nil {
			return 0, err
		}
		if tss <= 0 {
			return defaultWeeklyTarget, nil
		}
		return math.Round(tss), nil
	}

	values := make([]float64, 0, 4)
	for i := 3; i >= 0; i-- {
		tss, err := weeklyTSS(db, lastFull.AddDate(0, 0, -7*i))
		if err != nil {
			return 0, err
		}
		values = append(values, tss)
	}
	target := math.Round(median(values))
	if target <= 0 {
		return defaultWeeklyTarget, nil
	}
	return target, nil
}

// SetWeeklyTarget сохраняет ручную цель недельного TSS. Значение вне
// 100…1500 отклоняется: цель 0 или 10000 сломала бы и сводку, и рекомендации.
func SetWeeklyTarget(db DBTX, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < WeeklyTargetMin || v > WeeklyTargetMax {
		return fmt.Errorf("недельная цель TSS: нужно %.0f…%.0f, получено %g",
			WeeklyTargetMin, WeeklyTargetMax, v)
	}
	_, err := db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
	    ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		weeklyTargetKey, strconv.FormatFloat(v, 'f', -1, 64))
	return err
}

func weeklyTargetSetting(db DBTX) (float64, bool, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, weeklyTargetKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false, fmt.Errorf("settings.%s=%q: %w", weeklyTargetKey, raw, err)
	}
	return v, true, nil
}

// firstWorkoutDay — локальный день самой ранней тренировки.
func firstWorkoutDay(db DBTX) (time.Time, bool, error) {
	var start sql.NullString
	if err := db.QueryRow(`SELECT MIN(start_time) FROM workouts`).Scan(&start); err != nil {
		return time.Time{}, false, err
	}
	if !start.Valid {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, start.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return StartOfDay(t.Local()), true, nil
}

// weeklyTSS — сумма TSS за календарную неделю, начинающуюся с monday.
func weeklyTSS(db DBTX, monday time.Time) (float64, error) {
	end := monday.AddDate(0, 0, 7)
	var tss float64
	err := db.QueryRow(`SELECT COALESCE(SUM(COALESCE(tss, 0)), 0) FROM workouts
	    WHERE start_time >= ? AND start_time < ?`,
		monday.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)).Scan(&tss)
	if err != nil {
		return 0, err
	}
	return tss, nil
}

// median — медиана набора; для чётного количества — среднее двух центральных.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
