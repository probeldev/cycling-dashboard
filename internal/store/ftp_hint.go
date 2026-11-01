package store

import (
	"database/sql"
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
	    WHERE r.window_sec = 1200 AND w.start_time <= ?
	    ORDER BY r.watts DESC, w.start_time ASC, w.id ASC`, now.UTC().Format(time.RFC3339))
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
		// порог сравниваем по неокруглённой дельте: +2.01…2.04% должны
		// показываться, хотя в подписи они округляются до +2.0%
		deltaPct := (estimate/current - 1) * 100
		if deltaPct <= FTPHintThresholdPct+1e-9 {
			continue
		}
		hints = append(hints, FTPHint{
			Kind:      kind,
			Current:   roundHint(current),
			Estimate:  estimate,
			DeltaPct:  roundHint(deltaPct),
			WorkoutID: bests[i].id,
			Date:      bests[i].date,
		})
	}
	// API отдаёт одну подсказку: первым идёт самое заметное превышение
	sort.SliceStable(hints, func(i, j int) bool { return hints[i].DeltaPct > hints[j].DeltaPct })
	return hints, nil
}

// roundHint — ватты и проценты с одним знаком для подписи. Порог при этом
// сравнивается по неокруглённой дельте: иначе 2.01–2.04% прятались бы под
// подписью «+2.0%».
func roundHint(v float64) float64 {
	return math.Round(v*10) / 10
}

// ApplyFTPHintTx применяет подсказку «Обновить FTP» в переданной транзакции.
// Запись профиля действует с начала тренировки-источника: она и все следующие
// тренировки того же вида перештамповываются, прошлые — нет. Повторный вызов с
// теми же датой и значением не создаёт дубль и возвращает false.
func ApplyFTPHintTx(tx *sql.Tx, kind string, value float64, source *Workout) (bool, error) {
	profileKind := "ftp"
	if kind == "outdoor" {
		profileKind = "ftp_outdoor"
	}
	at := source.StartTime.UTC()
	// У станка нет строгого поиска: если запись оказывается самой ранней,
	// fallback применил бы новое значение и к тренировкам до источника
	// (включая уличные без ftp_outdoor). Держим прежнее значение отдельной
	// служебной записью за секунду до источника — так прошлое не
	// перештамповывается ни сейчас, ни при следующем старте. Граница нужна,
	// только если до источника нет записи строго раньше: запись ровно в
	// start_time тоже делает новую самой ранней при tie-break по id.
	if profileKind == "ftp" {
		before, err := hasProfileBefore(tx, profileKind, at)
		if err != nil {
			return false, err
		}
		if !before {
			if prev, ok := ProfileAt(tx, profileKind, at); ok {
				if _, err := insertProfileEntryOnceTx(tx, profileKind, prev, at.Add(-time.Second), true); err != nil {
					return false, err
				}
			}
		}
	}
	inserted, err := insertProfileEntryOnceTx(tx, profileKind, value, at, false)
	if err != nil {
		return false, err
	}
	if err := backfillProfileStampsTx(tx); err != nil {
		return false, err
	}
	return inserted, nil
}

// hasProfileBefore — есть ли запись вида строго раньше t. Запись ровно в t
// не считается: новая запись с тем же recorded_at выиграла бы tie-break по id
// и через fallback изменила бы прошлое.
func hasProfileBefore(q DBTX, kind string, t time.Time) (bool, error) {
	var exists bool
	err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM profile_history
	    WHERE kind = ? AND recorded_at < ?)`, kind, t.UTC().Format(time.RFC3339)).Scan(&exists)
	return exists, err
}

// insertProfileEntryOnceTx вставляет запись, только если такой же
// (kind, value, recorded_at) ещё нет: повторное нажатие кнопки идемпотентно.
func insertProfileEntryOnceTx(tx *sql.Tx, kind string, value float64, at time.Time, synthetic bool) (bool, error) {
	ts := at.UTC().Format(time.RFC3339)
	res, err := tx.Exec(`INSERT INTO profile_history (kind, value, recorded_at, synthetic)
	    SELECT ?, ?, ?, ? WHERE NOT EXISTS (
	      SELECT 1 FROM profile_history WHERE kind = ? AND value = ? AND recorded_at = ?
	    )`, kind, value, ts, synthetic, kind, value, ts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
