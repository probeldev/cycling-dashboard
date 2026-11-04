package store

import "time"

// LongestRide — самая длинная поездка: километры, локальное время старта и id
// тренировки. Указатель в ReadinessStats: null значит «таких поездок нет».
type LongestRide struct {
	KM        float64   `json:"km"`
	Date      time.Time `json:"date"`
	WorkoutID int64     `json:"workoutId"`
}

// ReadinessRolling — нагрузка за скользящее окно дней, включая сегодня.
type ReadinessRolling struct {
	KM    float64 `json:"km"`
	Hours float64 `json:"hours"`
	TSS   float64 `json:"tss"`
	Rides int     `json:"rides"`
}

// ReadinessStats — бревет-показатели по тренировкам.
// Окна — локальные календарные дни, включая сегодня: 7 дней — [today-6, today],
// 28 — [today-27, today], 90 — [today-89, today]. Пустые значения (нет
// подходящих поездок) — null, а не ноль: ноль км или ккал/ч неотличим от
// реального нуля. centuryCount — поездки от 100 км включительно за всё время.
type ReadinessStats struct {
	Longest90d     *LongestRide     `json:"longest90d"`
	LongestEver    *LongestRide     `json:"longestEver"`
	Rolling7       ReadinessRolling `json:"rolling7"`
	Rolling28      ReadinessRolling `json:"rolling28"`
	KcalPerHour    *float64         `json:"kcalPerHour"`
	AscentPer100km *float64         `json:"ascentPer100km"`
	LongestStreak  int              `json:"longestStreak"`
	CenturyCount   int              `json:"centuryCount"`
}

// Readiness считает показатели готовности к длинным дистанциям. Калории
// взвешиваются по времени: Σккал / Σсекунд × 3600 (поездки без калорий не
// входят ни в числитель, ни в знаменатель). Набор на 100 км — Σнабор /
// Σкилометры × 100 по поездкам с ненулевой дистанцией; при нулевой сумме
// дистанций — null, без деления на ноль. Серия дней — самый длинный отрезок
// подряд идущих локальных дней с тренировками за последний год.
func Readiness(db DBTX, now time.Time) (ReadinessStats, error) {
	today := StartOfDay(now)
	yearFrom := today.AddDate(0, 0, -364)
	day90 := today.AddDate(0, 0, -89)
	day28 := today.AddDate(0, 0, -27)
	day7 := today.AddDate(0, 0, -6)

	rows, err := db.Query(`SELECT id, start_time, COALESCE(distance_m, 0), duration_sec,
	    COALESCE(tss, tss_est, 0), COALESCE(calories, 0), COALESCE(total_ascent, 0)
	    FROM workouts WHERE start_time <= ?
	    ORDER BY start_time ASC, id ASC`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return ReadinessStats{}, err
	}
	defer rows.Close()

	var out ReadinessStats
	var kcal90, kcalSecs int64
	var ascent90, dist90M float64
	streakDays := map[time.Time]bool{}

	for rows.Next() {
		var id, duration int64
		var start string
		var distM, tss float64
		var calories, ascent int
		if err := rows.Scan(&id, &start, &distM, &duration, &tss, &calories, &ascent); err != nil {
			return ReadinessStats{}, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return ReadinessStats{}, err
		}
		lt := t.Local()
		if lt.After(now) {
			continue
		}
		km := distM / 1000
		if distM > 0 {
			if distM >= 100_000 {
				out.CenturyCount++
			}
			// при равенстве остаётся более ранняя поездка: обход по start_time ASC
			if out.LongestEver == nil || km > out.LongestEver.KM {
				out.LongestEver = &LongestRide{KM: km, Date: lt, WorkoutID: id}
			}
		}

		day := StartOfDay(lt)
		if !day.Before(yearFrom) {
			streakDays[day] = true
		}
		if day.Before(day90) {
			continue
		}
		if distM > 0 && (out.Longest90d == nil || km > out.Longest90d.KM) {
			out.Longest90d = &LongestRide{KM: km, Date: lt, WorkoutID: id}
		}
		if calories > 0 && duration > 0 {
			kcal90 += int64(calories)
			kcalSecs += duration
		}
		if distM > 0 {
			ascent90 += float64(ascent)
			dist90M += distM
		}
		if !day.Before(day28) {
			addRolling(&out.Rolling28, km, duration, tss)
		}
		if !day.Before(day7) {
			addRolling(&out.Rolling7, km, duration, tss)
		}
	}
	if err := rows.Err(); err != nil {
		return ReadinessStats{}, err
	}

	if kcalSecs > 0 {
		v := float64(kcal90) * 3600 / float64(kcalSecs)
		out.KcalPerHour = &v
	}
	if dist90M > 0 {
		v := ascent90 / (dist90M / 1000) * 100
		out.AscentPer100km = &v
	}
	run := 0
	for d := yearFrom; !d.After(today); d = d.AddDate(0, 0, 1) {
		if streakDays[d] {
			run++
			if run > out.LongestStreak {
				out.LongestStreak = run
			}
		} else {
			run = 0
		}
	}
	return out, nil
}

func addRolling(r *ReadinessRolling, km float64, duration int64, tss float64) {
	r.KM += km
	r.Hours += float64(duration) / 3600
	r.TSS += tss
	r.Rides++
}
