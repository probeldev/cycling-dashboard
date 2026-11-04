package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
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
)

// TargetSource — откуда взялась недельная цель. none означает, что цели нет:
// оценка текущей недели целью не считается.
type TargetSource string

const (
	TargetSourceManual   TargetSource = "manual"
	TargetSourceMedian4  TargetSource = "median4"
	TargetSourceLastFull TargetSource = "lastFull"
	TargetSourceNone     TargetSource = "none"
)

// WeeklyTargetInfo — цель недельного TSS и её источник.
type WeeklyTargetInfo struct {
	TSS    float64      `json:"tss"`
	Source TargetSource `json:"source"`
}

// WeeklyTarget — цель недельного TSS. Приоритет: явная настройка в settings,
// затем медиана последних четырёх полных недель с нагрузкой (TSS > 0), затем
// последняя полная неделя с нагрузкой, если истории меньше четырёх недель.
// Полностью пустая история или пустые полные недели дают none: текущая неделя
// не может быть целью сама себе.
func WeeklyTarget(db DBTX, now time.Time) (WeeklyTargetInfo, error) {
	if v, ok, err := weeklyTargetSetting(db); err != nil {
		return WeeklyTargetInfo{}, err
	} else if ok {
		return WeeklyTargetInfo{TSS: v, Source: TargetSourceManual}, nil
	}

	first, ok, err := firstWorkoutDay(db, now)
	if err != nil {
		return WeeklyTargetInfo{}, err
	}
	if !ok {
		return WeeklyTargetInfo{Source: TargetSourceNone}, nil
	}

	lastFull := WeekStart(now).AddDate(0, 0, -7)
	firstWeek := WeekStart(first)
	if lastFull.Before(firstWeek) {
		return WeeklyTargetInfo{Source: TargetSourceNone}, nil
	}
	weeks := calendarDaysBetween(firstWeek, lastFull)/7 + 1

	values := make([]float64, 0, 4)
	for i := 3; i >= 0; i-- {
		tss, err := weeklyTSS(db, lastFull.AddDate(0, 0, -7*i), now)
		if err != nil {
			return WeeklyTargetInfo{}, err
		}
		if tss > 0 {
			values = append(values, tss)
		}
	}
	if len(values) == 0 {
		return WeeklyTargetInfo{Source: TargetSourceNone}, nil
	}
	if weeks < 4 {
		return autoWeeklyTarget(values[len(values)-1], TargetSourceLastFull), nil
	}
	return autoWeeklyTarget(median(values), TargetSourceMedian4), nil
}

// autoWeeklyTarget округляет авто-цель до целого TSS. Значение меньше 0.5
// округлилось бы до нуля и дало бы «0 из 0 TSS» — такую цель считаем
// незаданной.
func autoWeeklyTarget(v float64, source TargetSource) WeeklyTargetInfo {
	rounded := math.Round(v)
	if rounded <= 0 {
		return WeeklyTargetInfo{Source: TargetSourceNone}
	}
	return WeeklyTargetInfo{TSS: rounded, Source: source}
}

// ErrWeeklyTargetRange — ошибка валидации ручной цели, а не сбой БД:
// обработчик отличает её от 500.
var ErrWeeklyTargetRange = errors.New("недельная цель TSS вне диапазона")

// SetWeeklyTarget сохраняет ручную цель недельного TSS. Значение вне
// 100…1500 отклоняется: цель 0 или 10000 сломала бы и сводку, и рекомендации.
func SetWeeklyTarget(db DBTX, v float64) error {
	if !validWeeklyTarget(v) {
		return fmt.Errorf("%w: нужно %.0f…%.0f, получено %g",
			ErrWeeklyTargetRange, WeeklyTargetMin, WeeklyTargetMax, v)
	}
	_, err := db.Exec(`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
	    ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		weeklyTargetKey, strconv.FormatFloat(v, 'f', -1, 64))
	return err
}

// ClearWeeklyTarget удаляет ручную цель: дальше WeeklyTarget снова считает
// автоматическую.
func ClearWeeklyTarget(db DBTX) error {
	_, err := db.Exec(`DELETE FROM settings WHERE key = ?`, weeklyTargetKey)
	return err
}

func validWeeklyTarget(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= WeeklyTargetMin && v <= WeeklyTargetMax
}

// weeklyTargetSetting читает ручную цель. Битое значение (не число, NaN, вне
// диапазона) не роняет дашборд: пишем лог и переходим на автоматическую цель.
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
	if err != nil || !validWeeklyTarget(v) {
		log.Printf("settings.%s=%q: некорректная цель, использую автоматическую", weeklyTargetKey, raw)
		return 0, false, nil
	}
	return v, true, nil
}

// firstWorkoutDay — локальный день самой ранней тренировки не позже now.
func firstWorkoutDay(db DBTX, now time.Time) (time.Time, bool, error) {
	var start sql.NullString
	if err := db.QueryRow(`SELECT MIN(start_time) FROM workouts WHERE start_time <= ?`,
		now.UTC().Format(time.RFC3339)).Scan(&start); err != nil {
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

// weeklyTSS — сумма TSS за календарную неделю, начинающуюся с monday,
// с учётом только тренировок не позже now. TSS эффективный: по мощности —
// обычный, без мощности — оценка по пульсу (tss_est).
func weeklyTSS(db DBTX, monday, now time.Time) (float64, error) {
	end := monday.AddDate(0, 0, 7)
	var tss float64
	err := db.QueryRow(`SELECT COALESCE(SUM(COALESCE(tss, tss_est, 0)), 0) FROM workouts
	    WHERE start_time >= ? AND start_time < ? AND start_time <= ?`,
		monday.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339),
		now.UTC().Format(time.RFC3339)).Scan(&tss)
	if err != nil {
		return 0, err
	}
	return tss, nil
}

// calendarDaysBetween считает календарные дни, а не часы: в день перехода на
// летнее время сутки длятся 23 часа, и деление длительности на 24 дало бы
// неверное число недель.
func calendarDaysBetween(from, to time.Time) int {
	loc := from.Location()
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, loc)
	n := 0
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
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
