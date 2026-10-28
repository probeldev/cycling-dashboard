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
	AvgCadence  int
	MaxCadence  int
	AvgPower    int
	MaxPower    int
	NormPower   int
	AvgHR       int
	MaxHR       int
	Calories    int
	HasPower    bool
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
    speed_ms    REAL
);
CREATE INDEX IF NOT EXISTS idx_samples_w ON samples(workout_id, ts_offset);
`)
	if err != nil {
		return err
	}
	// миграция: колонка name для пользовательских названий треков
	var hasName bool
	rows, err := db.Query(`PRAGMA table_info(workouts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "name" {
			hasName = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasName {
		_, err = db.Exec(`ALTER TABLE workouts ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	}
	return err
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
	res, err := db.Exec(`INSERT INTO workouts
	    (file_hash, file_path, file_mtime, start_time, year, month, has_gps, trainer,
	     duration_sec, distance_m, avg_speed_ms, avg_cadence, max_cadence,
	     avg_power, max_power, norm_power, avg_hr, max_hr, calories)
	    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	    ON CONFLICT(file_hash) DO UPDATE SET
	      file_path=excluded.file_path, file_mtime=excluded.file_mtime,
	      start_time=excluded.start_time, year=excluded.year, month=excluded.month,
	      has_gps=excluded.has_gps, trainer=excluded.trainer,
	      duration_sec=excluded.duration_sec, distance_m=excluded.distance_m,
	      avg_speed_ms=excluded.avg_speed_ms, avg_cadence=excluded.avg_cadence,
	      max_cadence=excluded.max_cadence, avg_power=excluded.avg_power,
	      max_power=excluded.max_power, norm_power=excluded.norm_power,
	      avg_hr=excluded.avg_hr, max_hr=excluded.max_hr, calories=excluded.calories`,
		hash, path, mtime, w.StartTime.UTC().Format(time.RFC3339),
		w.StartTime.Year(), int(w.StartTime.Month()), gps, tr,
		int64(w.Duration.Seconds()), dist,
		nullableFloat(w.AvgSpeedKmh/3.6), nullableInt(w.AvgCadence), nullableInt(w.MaxCadence),
		nullableInt(w.AvgPower), nullableInt(w.MaxPower), nullableInt(w.NormPower),
		nullableInt(w.AvgHR), nullableInt(w.MaxHR), nullableInt(w.Calories))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
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
	Offset  int64
	Lat     float64
	Lon     float64
	HasPos  bool
	Power   int
	Cadence int
	HR      int
	SpeedMs float64
}

func InsertSamples(db *sql.DB, workoutID int64, samples []Sample) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO samples (workout_id, ts_offset, lat, lon, power, cadence, hr, speed_ms)
		VALUES (?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range samples {
		var lat, lon any
		if s.HasPos {
			lat, lon = s.Lat, s.Lon
		}
		if _, err := stmt.Exec(workoutID, s.Offset, lat, lon,
			nullableInt(s.Power), nullableInt(s.Cadence), nullableInt(s.HR), nullableFloat(s.SpeedMs)); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	var spd sql.NullFloat64
	var cad, mcad, ap, mp, np, hr, mhr, kcal sql.NullInt64
	var tr, gps int
	var ov sql.NullInt64
	err := db.QueryRow(`SELECT id, start_time, name, duration_sec, distance_m, has_gps, trainer,
	    trainer_override, avg_speed_ms, avg_cadence, max_cadence, avg_power, max_power,
	    norm_power, avg_hr, max_hr, calories
	    FROM workouts WHERE id = ?`, id).Scan(
		&w.ID, &start, &w.Name, &durSec, &dist, &gps, &tr, &ov,
		&spd, &cad, &mcad, &ap, &mp, &np, &hr, &mhr, &kcal)
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
	w.AvgCadence = int(cad.Int64)
	w.MaxCadence = int(mcad.Int64)
	w.AvgPower = int(ap.Int64)
	w.MaxPower = int(mp.Int64)
	w.NormPower = int(np.Int64)
	w.AvgHR = int(hr.Int64)
	w.MaxHR = int(mhr.Int64)
	w.Calories = int(kcal.Int64)
	w.HasPower = ap.Valid
	return &w, nil
}

// Samples возвращает сэмплы с прореживанием, чтобы в ответе было не больше maxN точек.
func Samples(db *sql.DB, id int64, maxN int) ([]Sample, error) {
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE workout_id = ?`, id).Scan(&total); err != nil {
		return nil, err
	}
	stride := total/maxN + 1
	rows, err := db.Query(`SELECT ts_offset, lat, lon, power, cadence, hr, speed_ms
	    FROM samples WHERE workout_id = ? AND ts_offset % ? = 0 ORDER BY ts_offset`, id, stride)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var s Sample
		var lat, lon, spd sql.NullFloat64
		var p, c, h sql.NullInt64
		if err := rows.Scan(&s.Offset, &lat, &lon, &p, &c, &h, &spd); err != nil {
			return nil, err
		}
		s.Lat = lat.Float64
		s.Lon = lon.Float64
		s.HasPos = lat.Valid && lon.Valid
		s.Power = int(p.Int64)
		s.Cadence = int(c.Int64)
		s.HR = int(h.Int64)
		s.SpeedMs = spd.Float64
		out = append(out, s)
	}
	return out, rows.Err()
}
