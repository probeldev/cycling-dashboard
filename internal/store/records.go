package store

import "time"

// Record — лучшее значение окна MMP за период.
type Record struct {
	WindowSec int       `json:"secs"`
	Watts     float64   `json:"watts"`
	WorkoutID int64     `json:"workoutId"`
	Date      time.Time `json:"date"`
	Name      string    `json:"name"`
	Previous  *Record   `json:"previous,omitempty"`
}

// BestRecords — лучшие значения по окнам MMP за [from, to). При равенстве
// побеждает более ранняя тренировка: рекорд принадлежит тому, кто установил
// его первым. Результат отсортирован по окну. Для окон «на сейчас» нужен
// BestRecordsUntil: полуоткрытый to=now теряет тренировку, начавшуюся в
// текущую секунду.
func BestRecords(db DBTX, from, to time.Time) ([]Record, error) {
	return bestRecords(db, from, to, time.Time{})
}

// BestRecordsUntil — лучшие значения за [from, until] с включительной верхней
// границей: тренировка ровно в until входит, будущая — нет. Используется для
// рекордов и CP-фита «на сейчас», чтобы будущая тренировка не протекала в
// текущие показатели.
func BestRecordsUntil(db DBTX, from, until time.Time) ([]Record, error) {
	return bestRecords(db, from, time.Time{}, until)
}

func bestRecords(db DBTX, from, to, until time.Time) ([]Record, error) {
	query := `SELECT r.window_sec, r.watts, w.id, w.start_time, COALESCE(w.name, '')
	    FROM power_records r JOIN workouts w ON w.id = r.workout_id
	    WHERE w.start_time >= ?`
	args := []any{from.UTC().Format(time.RFC3339)}
	if !to.IsZero() {
		query += ` AND w.start_time < ?`
		args = append(args, to.UTC().Format(time.RFC3339))
	}
	if !until.IsZero() {
		query += ` AND w.start_time <= ?`
		args = append(args, until.UTC().Format(time.RFC3339))
	}
	query += ` ORDER BY r.window_sec, r.watts DESC, w.start_time ASC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int]bool{}
	out := []Record{}
	for rows.Next() {
		var r Record
		var start string
		if err := rows.Scan(&r.WindowSec, &r.Watts, &r.WorkoutID, &start, &r.Name); err != nil {
			return nil, err
		}
		if seen[r.WindowSec] {
			continue
		}
		seen[r.WindowSec] = true
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, err
		}
		r.Date = t.Local()
		out = append(out, r)
	}
	return out, rows.Err()
}

// WorkoutPRs — для каждого окна тренировки список периодов, где она держит
// рекорд: all, 365d, 90d, 30d (по убыванию значимости; равенство — у более
// ранней тренировке). Четыре скана таблицы (по одному на период) — осознанно:
// power_records маленькая (окна × тренировки с мощностью), а запрос только на
// деталке. Верхняя граница — now: будущая тренировка не получает PR и не
// отнимает рекорды у прошлых.
func WorkoutPRs(db DBTX, id int64, now time.Time) (map[int][]string, error) {
	periods := []struct {
		label string
		from  time.Time
	}{
		{"all", time.Time{}},
		{"365d", StartOfDay(now).AddDate(0, 0, -364)},
		{"90d", StartOfDay(now).AddDate(0, 0, -89)},
		{"30d", StartOfDay(now).AddDate(0, 0, -29)},
	}
	out := map[int][]string{}
	for _, p := range periods {
		best, err := BestRecordsUntil(db, p.from, now)
		if err != nil {
			return nil, err
		}
		for _, r := range best {
			if r.WorkoutID == id {
				out[r.WindowSec] = append(out[r.WindowSec], p.label)
			}
		}
	}
	return out, nil
}
