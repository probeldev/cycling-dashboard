package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Workout struct {
	ID         int64
	StartTime  time.Time
	Name       string
	Duration   time.Duration
	DistanceKm float64
	HasGPS     bool
	Trainer    bool
	Override   *bool

	AvgSpeedKmh float64
	MaxSpeedKmh float64
	AvgCadence  int
	MaxCadence  int
	AvgPower    int
	MaxPower    int
	NormPower   int
	AvgHR       int
	MaxHR       int
	Calories    int
	AscentM     int
	DescentM    int
	HasPower    bool

	// штампы профиля на момент тренировки — зоны считаются от них,
	// чтобы история не пересчитывалась при изменении FTP/веса
	FTP      int
	WeightKg float64
}

type MonthAgg struct {
	Year    int
	Month   time.Month
	Outdoor *Agg
	Trainer *Agg
}

type Agg struct {
	DistanceKm float64
	Hours      float64
	Calories   int
	AvgPower   int
}

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS workouts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    file_hash     TEXT NOT NULL UNIQUE,
    file_path     TEXT NOT NULL,
    file_mtime    INTEGER NOT NULL,
    start_time    TEXT NOT NULL,
    year          INTEGER NOT NULL,
    month         INTEGER NOT NULL,
    subsport      TEXT NOT NULL DEFAULT '',
    has_gps       INTEGER NOT NULL,
    trainer       INTEGER NOT NULL,
    trainer_override INTEGER,
    duration_sec  INTEGER NOT NULL,
    distance_m    INTEGER,
    avg_speed_ms  REAL,
    avg_cadence   INTEGER,
    max_cadence   INTEGER,
    avg_power     INTEGER,
    max_power     INTEGER,
    norm_power    INTEGER,
    avg_hr        INTEGER,
    max_hr        INTEGER,
    calories      INTEGER,
    max_speed_ms  REAL,
    total_ascent  INTEGER,
    total_descent INTEGER,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_workouts_ym ON workouts(year, month, start_time);

CREATE TABLE IF NOT EXISTS samples (
    workout_id  INTEGER NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    ts_offset   INTEGER NOT NULL,
    lat         REAL,
    lon         REAL,
    power       INTEGER,
    cadence     INTEGER,
    hr          INTEGER,
    speed_ms    REAL,
    altitude_m  REAL,
    distance_m  REAL
);
CREATE INDEX IF NOT EXISTS idx_samples_w ON samples(workout_id, ts_offset);

CREATE TABLE IF NOT EXISTS profile_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL, -- 'weight' (кг) или 'ftp' (Вт)
    value       REAL NOT NULL,
    recorded_at TEXT NOT NULL, -- с какой даты значение действует
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_profile_history ON profile_history(kind, recorded_at);
`)
	if err != nil {
		return err
	}
	// миграции: новые колонки в существующих базах
	for _, m := range []struct{ table, column, ddl string }{
		{"workouts", "name", `ALTER TABLE workouts ADD COLUMN name TEXT NOT NULL DEFAULT ''`},
		{"workouts", "max_speed_ms", `ALTER TABLE workouts ADD COLUMN max_speed_ms REAL`},
		{"workouts", "total_ascent", `ALTER TABLE workouts ADD COLUMN total_ascent INTEGER`},
		{"workouts", "total_descent", `ALTER TABLE workouts ADD COLUMN total_descent INTEGER`},
		{"samples", "altitude_m", `ALTER TABLE samples ADD COLUMN altitude_m REAL`},
		{"samples", "distance_m", `ALTER TABLE samples ADD COLUMN distance_m REAL`},
		{"workouts", "ftp_w", `ALTER TABLE workouts ADD COLUMN ftp_w INTEGER`},
		{"workouts", "weight_kg", `ALTER TABLE workouts ADD COLUMN weight_kg REAL`},
	} {
		has, err := hasColumn(db, m.table, m.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := db.Exec(m.ddl); err != nil {
				return err
			}
		}
	}
	return BackfillProfileStamps(db)
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// Changed возвращает true, если файла с таким хешем ещё нет или изменился.
func Changed(db *sql.DB, hash string, mtime int64) (bool, error) {
	var stored int64
	err := db.QueryRow(`SELECT file_mtime FROM workouts WHERE file_hash = ?`, hash).Scan(&stored)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return stored != mtime, nil
}

func InsertWorkout(db *sql.DB, w *Workout, hash, path string, mtime int64) (int64, error) {
	tr := 0
	if w.Trainer {
		tr = 1
	}
	gps := 0
	if w.HasGPS {
		gps = 1
	}
	dist := sql.NullInt64{}
	if w.DistanceKm > 0 {
		dist = sql.NullInt64{Int64: int64(w.DistanceKm * 1000), Valid: true}
	}
	_, err := db.Exec(`INSERT INTO workouts
	    (file_hash, file_path, file_mtime, start_time, year, month, has_gps, trainer,
	     duration_sec, distance_m, avg_speed_ms, max_speed_ms, avg_cadence, max_cadence,
	     avg_power, max_power, norm_power, avg_hr, max_hr, calories, total_ascent, total_descent,
	     ftp_w, weight_kg)
	    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	    ON CONFLICT(file_hash) DO UPDATE SET
	      file_path=excluded.file_path, file_mtime=excluded.file_mtime,
	      start_time=excluded.start_time, year=excluded.year, month=excluded.month,
	      has_gps=excluded.has_gps,
	      trainer=CASE WHEN trainer_override IS NOT NULL THEN trainer ELSE excluded.trainer END,
	      duration_sec=excluded.duration_sec, distance_m=excluded.distance_m,
	      avg_speed_ms=excluded.avg_speed_ms, max_speed_ms=excluded.max_speed_ms,
	      avg_cadence=excluded.avg_cadence, max_cadence=excluded.max_cadence,
	      avg_power=excluded.avg_power, max_power=excluded.max_power,
	      norm_power=excluded.norm_power, avg_hr=excluded.avg_hr, max_hr=excluded.max_hr,
	      calories=excluded.calories,
	      total_ascent=excluded.total_ascent, total_descent=excluded.total_descent,
	      ftp_w=excluded.ftp_w, weight_kg=excluded.weight_kg`,
		hash, path, mtime, w.StartTime.UTC().Format(time.RFC3339),
		w.StartTime.Year(), int(w.StartTime.Month()), gps, tr,
		int64(w.Duration.Seconds()), dist,
		nullableFloat(w.AvgSpeedKmh/3.6), nullableFloat(w.MaxSpeedKmh/3.6),
		nullableInt(w.AvgCadence), nullableInt(w.MaxCadence),
		nullableInt(w.AvgPower), nullableInt(w.MaxPower), nullableInt(w.NormPower),
		nullableInt(w.AvgHR), nullableInt(w.MaxHR), nullableInt(w.Calories),
		nullableInt(w.AscentM), nullableInt(w.DescentM),
		nullableInt(w.FTP), nullableFloat(w.WeightKg))
	if err != nil {
		return 0, err
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM workouts WHERE file_hash = ?`, hash).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := db.Exec(`DELETE FROM samples WHERE workout_id = ?`, id); err != nil {
		return 0, err
	}
	return id, nil
}

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullableFloat(v float64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func SetTrainerOverride(db *sql.DB, id int64, trainer bool) error {
	v := 0
	if trainer {
		v = 1
	}
	_, err := db.Exec(`UPDATE workouts SET trainer_override = ?, trainer = ? WHERE id = ?`, v, v, id)
	return err
}

type Sample struct {
	Offset    int64
	Lat       float64
	Lon       float64
	HasPos    bool
	Power     int
	HasPwr    bool // false = датчик отвалился (нет поля), true = есть значение (в т.ч. честный 0)
	Cadence   int
	HasCad    bool
	HR        int
	SpeedMs   float64
	AltitudeM float64
	HasAlt    bool
	DistanceM float64
	HasDist   bool
}

func InsertSamples(db *sql.DB, workoutID int64, samples []Sample) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO samples (workout_id, ts_offset, lat, lon, power, cadence, hr, speed_ms, altitude_m, distance_m)
		VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range samples {
		var lat, lon, alt, dist, pwr, cad any
		if s.HasPos {
			lat, lon = s.Lat, s.Lon
		}
		if s.HasAlt {
			alt = s.AltitudeM
		}
		if s.HasDist {
			dist = s.DistanceM
		}
		// power/каденс: NULL только при дропауте датчика, честный 0 храним как 0
		if s.HasPwr {
			pwr = s.Power
		}
		if s.HasCad {
			cad = s.Cadence
		}
		if _, err := stmt.Exec(workoutID, s.Offset, lat, lon,
			pwr, cad, nullableInt(s.HR), nullableFloat(s.SpeedMs),
			alt, dist); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ProfileEntry — запись истории веса или FTP.
type ProfileEntry struct {
	ID         int64
	Kind       string // 'weight' или 'ftp'
	Value      float64
	RecordedAt time.Time
}

// AddProfileEntry добавляет запись истории и пересчитывает штампы тренировок.
func AddProfileEntry(db *sql.DB, kind string, value float64, at time.Time) error {
	if _, err := db.Exec(`INSERT INTO profile_history (kind, value, recorded_at) VALUES (?,?,?)`,
		kind, value, at.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return BackfillProfileStamps(db)
}

// DeleteProfileEntry удаляет запись истории и пересчитывает штампы тренировок.
func DeleteProfileEntry(db *sql.DB, id int64) error {
	if _, err := db.Exec(`DELETE FROM profile_history WHERE id = ?`, id); err != nil {
		return err
	}
	return BackfillProfileStamps(db)
}

// ProfileHistory возвращает всю историю, новые записи первыми.
func ProfileHistory(db *sql.DB) ([]ProfileEntry, error) {
	rows, err := db.Query(`SELECT id, kind, value, recorded_at FROM profile_history
		ORDER BY recorded_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileEntry
	for rows.Next() {
		var e ProfileEntry
		var at string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Value, &at); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return nil, err
		}
		e.RecordedAt = t.Local()
		out = append(out, e)
	}
	return out, rows.Err()
}

// ProfileAt возвращает значение ('weight'/'ftp'), действовавшее на момент t:
// последнее до t, а если такого нет — самое раннее известное.
func ProfileAt(db *sql.DB, kind string, t time.Time) (float64, bool) {
	ts := t.UTC().Format(time.RFC3339)
	var v float64
	err := db.QueryRow(`SELECT value FROM profile_history
		WHERE kind = ? AND recorded_at <= ? ORDER BY recorded_at DESC LIMIT 1`, kind, ts).Scan(&v)
	if err == sql.ErrNoRows {
		err = db.QueryRow(`SELECT value FROM profile_history
			WHERE kind = ? ORDER BY recorded_at ASC LIMIT 1`, kind).Scan(&v)
	}
	if err != nil {
		return 0, false
	}
	return v, true
}

// BackfillProfileStamps проставляет тренировкам вес и FTP по истории профиля
// на дату каждой тренировки. Идемпотентен, вызывается после изменения истории.
func BackfillProfileStamps(db *sql.DB) error {
	for _, m := range []struct{ kind, column string }{
		{"ftp", "ftp_w"},
		{"weight", "weight_kg"},
	} {
		if _, err := db.Exec(`UPDATE workouts SET ` + m.column + ` = COALESCE(
			(SELECT value FROM profile_history
			 WHERE kind = ? AND recorded_at <= start_time
			 ORDER BY recorded_at DESC LIMIT 1),
			(SELECT value FROM profile_history
			 WHERE kind = ? ORDER BY recorded_at ASC LIMIT 1))`, m.kind, m.kind); err != nil {
			return err
		}
	}
	return nil
}

func SetName(db *sql.DB, id int64, name string) error {
	_, err := db.Exec(`UPDATE workouts SET name = ? WHERE id = ?`, name, id)
	return err
}

type WorkoutRow struct {
	ID          int64
	StartTime   time.Time
	Name        string
	DurationSec int64
	DistanceKm  float64
	Trainer     bool
	AvgPower    int
	AvgCadence  int
	Calories    int
}

type Year struct {
	Year    int
	Outdoor Agg
	Trainer Agg
	Months  []Month
}

type Month struct {
	Month         time.Month
	Outdoor       Agg
	Trainer       Agg
	TotalCalories int
	Rides         []WorkoutRow
}

func ListTree(db *sql.DB) ([]Year, error) {
	rows, err := db.Query(`SELECT id, start_time, name, duration_sec, distance_m, trainer,
	    avg_power, avg_cadence, calories
	    FROM workouts ORDER BY start_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var years []Year
	yearIdx := map[int]int{}
	monthIdx := map[int]map[int]int{}

	for rows.Next() {
		var r WorkoutRow
		var start string
		var dist sql.NullInt64
		var ap, ac, kcal sql.NullInt64
		if err := rows.Scan(&r.ID, &start, &r.Name, &r.DurationSec, &dist, &r.Trainer,
			&ap, &ac, &kcal); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return nil, err
		}
		t = t.Local()
		r.StartTime = t
		if dist.Valid {
			r.DistanceKm = float64(dist.Int64) / 1000
		}
		r.AvgPower = int(ap.Int64)
		r.AvgCadence = int(ac.Int64)
		r.Calories = int(kcal.Int64)

		yi, ok := yearIdx[t.Year()]
		if !ok {
			yi = len(years)
			yearIdx[t.Year()] = yi
			monthIdx[t.Year()] = map[int]int{}
			years = append(years, Year{Year: t.Year()})
		}
		y := &years[yi]
		mi, ok := monthIdx[t.Year()][int(t.Month())]
		if !ok {
			mi = len(y.Months)
			monthIdx[t.Year()][int(t.Month())] = mi
			y.Months = append(y.Months, Month{Month: t.Month()})
		}
		m := &y.Months[mi]
		m.Rides = append(m.Rides, r)

		hours := float64(r.DurationSec) / 3600
		agg := &y.Outdoor
		magg := &m.Outdoor
		if r.Trainer {
			agg = &y.Trainer
			magg = &m.Trainer
		}
		agg.DistanceKm += r.DistanceKm
		agg.Hours += hours
		agg.Calories += r.Calories
		magg.DistanceKm += r.DistanceKm
		magg.Hours += hours
		magg.Calories += r.Calories
		m.TotalCalories += r.Calories
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// взвешенная средняя мощность по месяцам и годам
	for yi := range years {
		y := &years[yi]
		y.Outdoor.AvgPower, y.Trainer.AvgPower = avgPower(db, `year = ? AND trainer = 0`, y.Year), avgPower(db, `year = ? AND trainer = 1`, y.Year)
		for mi := range y.Months {
			m := &y.Months[mi]
			m.Outdoor.AvgPower = avgPower(db, `year = ? AND month = ? AND trainer = 0`, y.Year, int(m.Month))
			m.Trainer.AvgPower = avgPower(db, `year = ? AND month = ? AND trainer = 1`, y.Year, int(m.Month))
		}
	}
	return years, nil
}

func avgPower(db *sql.DB, where string, args ...any) int {
	var v sql.NullInt64
	err := db.QueryRow(fmt.Sprintf(
		`SELECT SUM(avg_power*duration_sec)/SUM(duration_sec) FROM workouts
		 WHERE avg_power IS NOT NULL AND %s`, where), args...).Scan(&v)
	if err != nil || !v.Valid {
		return 0
	}
	return int(v.Int64)
}

func GetWorkout(db *sql.DB, id int64) (*Workout, error) {
	var w Workout
	var start string
	var durSec int64
	var dist sql.NullInt64
	var spd, mspd sql.NullFloat64
	var cad, mcad, ap, mp, np, hr, mhr, kcal, asc, desc, ftp sql.NullInt64
	var wkg sql.NullFloat64
	var tr, gps int
	var ov sql.NullInt64
	err := db.QueryRow(`SELECT id, start_time, name, duration_sec, distance_m, has_gps, trainer,
	    trainer_override, avg_speed_ms, max_speed_ms, avg_cadence, max_cadence, avg_power, max_power,
	    norm_power, avg_hr, max_hr, calories, total_ascent, total_descent, ftp_w, weight_kg
	    FROM workouts WHERE id = ?`, id).Scan(
		&w.ID, &start, &w.Name, &durSec, &dist, &gps, &tr, &ov,
		&spd, &mspd, &cad, &mcad, &ap, &mp, &np, &hr, &mhr, &kcal, &asc, &desc, &ftp, &wkg)
	if err != nil {
		return nil, err
	}
	w.Duration = time.Duration(durSec) * time.Second
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return nil, err
	}
	w.StartTime = t.Local()
	w.HasGPS = gps == 1
	w.Trainer = tr == 1
	if ov.Valid {
		b := ov.Int64 == 1
		w.Override = &b
	}
	if dist.Valid {
		w.DistanceKm = float64(dist.Int64) / 1000
	}
	if spd.Valid {
		w.AvgSpeedKmh = spd.Float64 * 3.6
	}
	if mspd.Valid {
		w.MaxSpeedKmh = mspd.Float64 * 3.6
	}
	w.AscentM = int(asc.Int64)
	w.DescentM = int(desc.Int64)
	w.AvgCadence = int(cad.Int64)
	w.MaxCadence = int(mcad.Int64)
	w.AvgPower = int(ap.Int64)
	w.MaxPower = int(mp.Int64)
	w.NormPower = int(np.Int64)
	w.AvgHR = int(hr.Int64)
	w.MaxHR = int(mhr.Int64)
	w.Calories = int(kcal.Int64)
	w.HasPower = ap.Valid
	w.FTP = int(ftp.Int64)
	w.WeightKg = wkg.Float64
	return &w, nil
}

// Samples возвращает сэмплы, уменьшая количество до ~maxN точек бакет-усреднением
// (а не выборкой каждой N-й — иначе графики страдают от алиасинга).
// При maxN >= количества сэмплов возвращает их как есть, по одной секунде.
func Samples(db *sql.DB, id int64, maxN int) ([]Sample, error) {
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE workout_id = ?`, id).Scan(&total); err != nil {
		return nil, err
	}
	bucket := total/maxN + 1
	rows, err := db.Query(`SELECT MIN(ts_offset), AVG(lat), AVG(lon),
	    CAST(AVG(power) AS INTEGER), CAST(AVG(cadence) AS INTEGER), CAST(AVG(hr) AS INTEGER),
	    AVG(speed_ms), AVG(altitude_m), MAX(distance_m)
	    FROM samples WHERE workout_id = ? GROUP BY ts_offset / ? ORDER BY 1`, id, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var s Sample
		var lat, lon, spd, alt, dist sql.NullFloat64
		var p, c, h sql.NullInt64
		if err := rows.Scan(&s.Offset, &lat, &lon, &p, &c, &h, &spd, &alt, &dist); err != nil {
			return nil, err
		}
		s.Lat = lat.Float64
		s.Lon = lon.Float64
		s.HasPos = lat.Valid && lon.Valid
		s.Power = int(p.Int64)
		s.HasPwr = p.Valid
		s.Cadence = int(c.Int64)
		s.HasCad = c.Valid
		s.HR = int(h.Int64)
		s.SpeedMs = spd.Float64
		s.AltitudeM = alt.Float64
		s.HasAlt = alt.Valid
		s.DistanceM = dist.Float64
		s.HasDist = dist.Valid
		out = append(out, s)
	}
	return out, rows.Err()
}
