package store

import (
	"math"
	"sort"
	"time"
)

// FTPHintThresholdPct — превышение оценки над действующим FTP, с которого
// стоит показывать подсказку. Разовая хорошая тренировка — это шум, а не повод
// менять FTP: порог строгий (ровно +2% ещё не подсказка).
const FTPHintThresholdPct = 2.0

// FTPHint — оценка FTP по лучшему 20-минутному окну (0.95 × W1200) и
// действующее значение того же вида.
type FTPHint struct {
	Kind      string    `json:"kind"` // trainer | outdoor
	Current   float64   `json:"current"`
	Estimate  float64   `json:"estimate"`
	DeltaPct  float64   `json:"deltaPct"`
	WorkoutID int64     `json:"workoutId"`
	Date      time.Time `json:"date"`
}

// FTPHints возвращает подсказки об обновлении FTP по лучшему окну 1200 с
// отдельно для станка и улицы. Тестовые тренировки участвуют: 20-минутный
// тест — это и есть замер, а ramp-тест окна 1200 с не даёт вовсе.
// Для станка сравниваем с ftp, для улицы — с ftp_outdoor (если его нет,
// с ftp). Порог — строго больше FTPHintThresholdPct; результат отсортирован
// по убыванию превышения.
func FTPHints(db DBTX, now time.Time) ([]FTPHint, error) {
	rows, err := db.Query(`SELECT w.trainer, r.watts, w.id, w.start_time
	    FROM power_records r JOIN workouts w ON w.id = r.workout_id
	    WHERE r.window_sec = 1200
	    ORDER BY r.watts DESC, w.start_time ASC, w.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type best struct {
		watts float64
		id    int64
		date  time.Time
	}
	var found [2]bool
	var bests [2]best
	for rows.Next() {
		var trainer bool
		var watts float64
		var id int64
		var start string
		if err := rows.Scan(&trainer, &watts, &id, &start); err != nil {
			return nil, err
		}
		i := 0
		if trainer {
			i = 1
		}
		if found[i] {
			continue
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, err
		}
		found[i] = true
		bests[i] = best{watts: watts, id: id, date: t.Local()}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hints := []FTPHint{}
	for i, trainer := range []bool{false, true} {
		if !found[i] {
			continue
		}
		kind := "outdoor"
		current, ok := ProfileAt(db, "ftp_outdoor", now)
		if trainer {
			kind = "trainer"
			current, ok = ProfileAt(db, "ftp", now)
		} else if !ok {
			// уличного FTP ещё нет — сравниваем со станковым, как в
			// effectiveFTPAt у штампов тренировок
			current, ok = ProfileAt(db, "ftp", now)
		}
		if !ok || current <= 0 {
			continue
		}
		estimate := roundHint(0.95 * bests[i].watts)
		delta := roundHint((estimate/current - 1) * 100)
		if delta <= FTPHintThresholdPct {
			continue
		}
		hints = append(hints, FTPHint{
			Kind:      kind,
			Current:   roundHint(current),
			Estimate:  estimate,
			DeltaPct:  delta,
			WorkoutID: bests[i].id,
			Date:      bests[i].date,
		})
	}
	// API отдаёт одну подсказку: первым идёт самое заметное превышение
	sort.SliceStable(hints, func(i, j int) bool { return hints[i].DeltaPct > hints[j].DeltaPct })
	return hints, nil
}

// roundHint — ватты и проценты с одним знаком: порог и подпись считаются от
// одного и того же округлённого значения, чтобы «+2.0%» не всплывало из-за
// хвоста float64.
func roundHint(v float64) float64 {
	return math.Round(v*10) / 10
}
