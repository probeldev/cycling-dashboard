package store

import (
	"database/sql"
	"strings"
	"time"
)

// Day — день таймлайна с PMC (Performance Management Chart).
// CTL — «форма» (хроническая нагрузка, EWMA 42 дня), ATL — «усталость»
// (7 дней), TSB — «свежесть» (CTL вчера минус ATL вчера, до нагрузки дня).
type Day struct {
	Date         time.Time `json:"date"` // локальная полночь
	TSS          float64   `json:"tss"`
	Hours        float64   `json:"hours"`
	DistanceKm   float64   `json:"distanceKm"`
	Rides        int       `json:"rides"`
	TrainerRides int       `json:"trainerRides"`
	OutdoorRides int       `json:"outdoorRides"`
	CTL          float64   `json:"ctl"`
	ATL          float64   `json:"atl"`
	TSB          float64   `json:"tsb"`
	Names        string    `json:"names,omitempty"` // названия тренировок дня для подсказок
	Tests        int       `json:"tests,omitempty"` // сколько из них помечено тестовыми (замер)
}

// Timeline — дневной ряд [from, to] включительно.
// CTL_t = CTL_{t-1} + (TSS_t − CTL_{t-1})/42, ATL_t = ATL_{t-1} + (TSS_t − ATL_{t-1})/7,
// TSB_t = CTL_{t-1} − ATL_{t-1}. Пустые дни идут с TSS=0 и участвуют в EWMA,
// поэтому пропуски не разрывают кривую.
// EWMA греется с даты самой ранней тренировки (или с from, если истории нет),
// а в ответ попадают только дни [from, to] — иначе CTL/TSB зависели бы от окна.
// День тренировки — локальная дата start_time (в БД время хранится в UTC).
func Timeline(db DBTX, from, to time.Time) ([]Day, error) {
	return timeline(db, from, to, time.Time{})
}

// TimelineUntil — Timeline, но тренировки позже until не учитываются. Нужен
// статусу и прогрессу: запись с будущим start_time не должна менять PMC,
// неделю и нагрузку «на сейчас».
func TimelineUntil(db DBTX, from, to, until time.Time) ([]Day, error) {
	return timeline(db, from, to, until)
}

func timeline(db DBTX, from, to, until time.Time) ([]Day, error) {
	from, to = StartOfDay(from), StartOfDay(to)
	if to.Before(from) {
		return nil, nil
	}
	warmFrom, err := warmStart(db, from)
	if err != nil {
		return nil, err
	}

	// запас по UTC с обеих сторон: локальная дата может отличаться от UTC
	qFrom := warmFrom.AddDate(0, 0, -2).UTC().Format(time.RFC3339)
	qTo := to.AddDate(0, 0, 3).UTC().Format(time.RFC3339)
	query := `SELECT start_time, duration_sec, COALESCE(distance_m, 0),
	    trainer, is_test, COALESCE(tss, 0), COALESCE(name, '')
	    FROM workouts WHERE start_time >= ? AND start_time < ?`
	args := []any{qFrom, qTo}
	if !until.IsZero() {
		query += ` AND start_time <= ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type agg struct {
		tss, hours, dist float64
		rides, trainer   int
		tests            int
		names            []string
	}
	byDay := map[time.Time]*agg{}
	for rows.Next() {
		var start string
		var dur int64
		var dist, tss float64
		var trainer, isTest bool
		var name string
		if err := rows.Scan(&start, &dur, &dist, &trainer, &isTest, &tss, &name); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, err
		}
		lt := t.Local()
		if lt.Before(warmFrom) || !lt.Before(to.AddDate(0, 0, 1)) {
			continue
		}
		d := StartOfDay(lt)
		a := byDay[d]
		if a == nil {
			a = &agg{}
			byDay[d] = a
		}
		a.tss += tss
		a.hours += float64(dur) / 3600
		a.dist += dist / 1000
		a.rides++
		if trainer {
			a.trainer++
		}
		if name != "" {
			a.names = append(a.names, name)
		}
		if isTest {
			// в PMC тест остаётся (нагрузка реальная) — в подсказке календаря
			// только помечаем его, чтобы не путать с рабочей тренировкой
			a.tests++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	days := make([]Day, 0, int(to.Sub(from).Hours()/24)+1)
	var ctl, atl float64
	for d := warmFrom; !d.After(to); d = d.AddDate(0, 0, 1) {
		day := Day{Date: d}
		if a := byDay[d]; a != nil {
			day.TSS = a.tss
			day.Hours = a.hours
			day.DistanceKm = a.dist
			day.Rides = a.rides
			day.TrainerRides = a.trainer
			day.OutdoorRides = a.rides - a.trainer
			day.Names = strings.Join(a.names, ", ")
			day.Tests = a.tests
		}
		// TSB — вчерашние CTL/ATL, до нагрузки текущего дня
		day.TSB = ctl - atl
		ctl += (day.TSS - ctl) / 42
		atl += (day.TSS - atl) / 7
		day.CTL = ctl
		day.ATL = atl
		if d.Before(from) {
			continue // день прогрева — в ответ не отдаём
		}
		days = append(days, day)
	}
	return days, nil
}

// warmStart — с какого дня греть EWMA, чтобы результат не зависел от окна.
// Берём локальную дату самой ранней тренировки; если она не раньше from или
// истории нет — from.
func warmStart(db DBTX, from time.Time) (time.Time, error) {
	var minStart sql.NullString
	if err := db.QueryRow(`SELECT MIN(start_time) FROM workouts`).Scan(&minStart); err != nil {
		return from, err
	}
	if !minStart.Valid {
		return from, nil
	}
	t, err := time.Parse(time.RFC3339, minStart.String)
	if err != nil {
		return from, err
	}
	if first := StartOfDay(t.Local()); first.Before(from) {
		return first, nil
	}
	return from, nil
}

// StartOfDay — локальная полночь даты t.
func StartOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}
