package store

import (
	"database/sql"
	"fmt"
	"log"
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
	SubSport   string // подсорт из FIT: generic, indoor_cycling, road, mountain…

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

	// версия алгоритма, которым посчитан NormPower (0 — старый, до окна 30 с);
	// по ней бэкфилл понимает, что тренировку надо пересчитать
	NPVersion int

	// метрики тренировочной нагрузки; нули в БД — это NULL («неизвестно»),
	// кроме MetricsVersion
	IF                float64 // NP / ftp_w
	TSS               float64 // duration_sec · NP · IF / (ftp_w · 3600) · 100
	VI                float64 // NP / avg_power
	AvgPowerActive    int     // средняя мощность только по power > 0
	CoastingSec       int     // время с power == 0
	NoPowerSec        int     // время без данных поверметра (power IS NULL)
	SuspectDropoutSec int     // power == 0 при cadence > 0 — похоже на отвал датчика
	ElapsedSec        int     // из session TotalElapsedTime
	PauseSec          int     // elapsed_sec - duration_sec
	WorkKJ            float64 // механическая работа, кДж
	MetricsVersion    int     // версия расчёта метрик, 0 — не считались
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
		{"workouts", "np_version", `ALTER TABLE workouts ADD COLUMN np_version INTEGER NOT NULL DEFAULT 0`},
		{"workouts", "subsport_checked", `ALTER TABLE workouts ADD COLUMN subsport_checked INTEGER NOT NULL DEFAULT 0`},
		{"workouts", "if", `ALTER TABLE workouts ADD COLUMN if REAL`},
		{"workouts", "tss", `ALTER TABLE workouts ADD COLUMN tss REAL`},
		{"workouts", "vi", `ALTER TABLE workouts ADD COLUMN vi REAL`},
		{"workouts", "avg_power_active", `ALTER TABLE workouts ADD COLUMN avg_power_active INTEGER`},
		{"workouts", "coasting_sec", `ALTER TABLE workouts ADD COLUMN coasting_sec INTEGER`},
		{"workouts", "no_power_sec", `ALTER TABLE workouts ADD COLUMN no_power_sec INTEGER`},
		{"workouts", "suspect_dropout_sec", `ALTER TABLE workouts ADD COLUMN suspect_dropout_sec INTEGER`},
		{"workouts", "elapsed_sec", `ALTER TABLE workouts ADD COLUMN elapsed_sec INTEGER`},
		{"workouts", "pause_sec", `ALTER TABLE workouts ADD COLUMN pause_sec INTEGER`},
		{"workouts", "work_kj", `ALTER TABLE workouts ADD COLUMN work_kj REAL`},
		{"workouts", "metrics_version", `ALTER TABLE workouts ADD COLUMN metrics_version INTEGER NOT NULL DEFAULT 0`},
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
	if err := cleanupOrphanSamples(db); err != nil {
		return err
	}
	return BackfillProfileStamps(db)
}

// cleanupOrphanSamples удаляет сэмплы, на которые нет тренировки: такие строки
// не видны ни одному запросу. Если workouts пуста, данные не трогаем — иначе
// недосозданная или частично восстановленная база потеряла бы все сэмплы.
// Идемпотентно: на чистой базе удаляется 0 строк.
func cleanupOrphanSamples(db *sql.DB) error {
	var hasWorkouts bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM workouts)`).Scan(&hasWorkouts); err != nil {
		return err
	}
	if !hasWorkouts {
		// предупреждаем, только если реально есть что чистить: на новой базе
		// пустая workouts — это норма
		var hasSamples bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM samples)`).Scan(&hasSamples); err != nil {
			return err
		}
		if hasSamples {
			log.Printf("миграция: чистка осиротевших сэмплов пропущена — таблица workouts пуста")
		}
		return nil
	}
	res, err := db.Exec(`DELETE FROM samples
		WHERE workout_id NOT IN (SELECT id FROM workouts)
		  AND EXISTS (SELECT 1 FROM workouts)`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		log.Printf("миграция: удалено %d осиротевших сэмплов", n)
	}
	return nil
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
	    (file_hash, file_path, file_mtime, start_time, year, month, subsport, subsport_checked, has_gps, trainer,
	     duration_sec, distance_m, avg_speed_ms, max_speed_ms, avg_cadence, max_cadence,
	     avg_power, max_power, norm_power, np_version, avg_hr, max_hr, calories,
	     total_ascent, total_descent, ftp_w, weight_kg,
	     if, tss, vi, avg_power_active, coasting_sec, no_power_sec, suspect_dropout_sec,
	     elapsed_sec, pause_sec, work_kj, metrics_version)
	    VALUES (?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	    ON CONFLICT(file_hash) DO UPDATE SET
	      file_path=excluded.file_path, file_mtime=excluded.file_mtime,
	      start_time=excluded.start_time, year=excluded.year, month=excluded.month,
	      subsport=excluded.subsport, subsport_checked=1, has_gps=excluded.has_gps,
	      trainer=CASE WHEN trainer_override IS NOT NULL THEN trainer ELSE excluded.trainer END,
	      duration_sec=excluded.duration_sec, distance_m=excluded.distance_m,
	      avg_speed_ms=excluded.avg_speed_ms, max_speed_ms=excluded.max_speed_ms,
	      avg_cadence=excluded.avg_cadence, max_cadence=excluded.max_cadence,
	      avg_power=excluded.avg_power, max_power=excluded.max_power,
	      norm_power=excluded.norm_power, np_version=excluded.np_version,
	      avg_hr=excluded.avg_hr, max_hr=excluded.max_hr,
	      calories=excluded.calories,
	      total_ascent=excluded.total_ascent, total_descent=excluded.total_descent,
	      ftp_w=excluded.ftp_w, weight_kg=excluded.weight_kg,
	      if=excluded.if, tss=excluded.tss, vi=excluded.vi,
	      avg_power_active=excluded.avg_power_active, coasting_sec=excluded.coasting_sec,
	      no_power_sec=excluded.no_power_sec, suspect_dropout_sec=excluded.suspect_dropout_sec,
	      elapsed_sec=excluded.elapsed_sec, pause_sec=excluded.pause_sec,
	      work_kj=excluded.work_kj, metrics_version=excluded.metrics_version`,
		hash, path, mtime, w.StartTime.UTC().Format(time.RFC3339),
		w.StartTime.Year(), int(w.StartTime.Month()), w.SubSport, gps, tr,
		int64(w.Duration.Seconds()), dist,
		nullableFloat(w.AvgSpeedKmh/3.6), nullableFloat(w.MaxSpeedKmh/3.6),
		nullableInt(w.AvgCadence), nullableInt(w.MaxCadence),
		nullableInt(w.AvgPower), nullableInt(w.MaxPower), nullableInt(w.NormPower), w.NPVersion,
		nullableInt(w.AvgHR), nullableInt(w.MaxHR), nullableInt(w.Calories),
		nullableInt(w.AscentM), nullableInt(w.DescentM),
		nullableInt(w.FTP), nullableFloat(w.WeightKg),
		nullableFloat(w.IF), nullableFloat(w.TSS), nullableFloat(w.VI),
		nullableInt(w.AvgPowerActive), nullableInt(w.CoastingSec), nullableInt(w.NoPowerSec),
		nullableInt(w.SuspectDropoutSec), nullableInt(w.ElapsedSec), nullableInt(w.PauseSec),
		nullableFloat(w.WorkKJ), w.MetricsVersion)
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
		if _, err := db.Exec(`UPDATE workouts SET `+m.column+` = COALESCE(
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

// FileRef — ссылка тренировки на исходный FIT-файл.
type FileRef struct {
	ID   int64
	Path string
}

// MissingSubSport возвращает тренировки, у которых subsport ещё не проверяли:
// колонка была в схеме с самого начала, но не заполнялась, поэтому старые записи
// нужно дочитать из FIT-файлов. subsport_checked ставится и когда в файле пусто —
// иначе один и тот же файл перечитывался бы при каждом старте.
func MissingSubSport(db *sql.DB) ([]FileRef, error) {
	rows, err := db.Query(`SELECT id, file_path FROM workouts WHERE subsport_checked = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var r FileRef
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetSubSport проставляет subsport и помечает строку проверенной, не трогая
// остальные поля — пользовательские name и trainer_override должны уцелеть.
func SetSubSport(db *sql.DB, id int64, subsport string) error {
	_, err := db.Exec(`UPDATE workouts SET subsport = ?, subsport_checked = 1 WHERE id = ?`, subsport, id)
	return err
}

// MarkSubSportChecked помечает строку проверенной, не трогая значение subsport:
// файл недоступен, но уже известный subsport терять нельзя.
func MarkSubSportChecked(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE workouts SET subsport_checked = 1 WHERE id = ?`, id)
	return err
}

// PowerSampleStats — сколько у тренировки сэмплов с валидной мощностью
// и сколько из них больше нуля. Дешёвая проверка перед вычитыванием сэмплов.
func PowerSampleStats(db *sql.DB, id int64) (total, nonZero int, err error) {
	err = db.QueryRow(`SELECT COUNT(*),
	    COALESCE(SUM(CASE WHEN power > 0 THEN 1 ELSE 0 END), 0)
	    FROM samples WHERE workout_id = ? AND power IS NOT NULL`, id).Scan(&total, &nonZero)
	return total, nonZero, err
}

// NPWorkout — тренировка, которой нужен пересчёт NP, и её device-NP.
type NPWorkout struct {
	ID        int64
	NormPower int // 0 — устройство NP не отдало
}

// WorkoutsNeedingNP — тренировки, у которых NP посчитан алгоритмом старше version.
func WorkoutsNeedingNP(db *sql.DB, version int) ([]NPWorkout, error) {
	rows, err := db.Query(`SELECT id, COALESCE(norm_power, 0) FROM workouts
	    WHERE np_version < ? ORDER BY id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NPWorkout
	for rows.Next() {
		var w NPWorkout
		if err := rows.Scan(&w.ID, &w.NormPower); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// NormPowerUpdate — новый NP и версия алгоритма для одной тренировки.
type NormPowerUpdate struct {
	ID      int64
	NP      int
	Version int
}

// WorkoutsNeedingMetrics — тренировки, у которых метрики нагрузки посчитаны
// версией старше version.
func WorkoutsNeedingMetrics(db *sql.DB, version int) ([]FileRef, error) {
	rows, err := db.Query(`SELECT id, file_path FROM workouts
	    WHERE metrics_version < ? ORDER BY id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var r FileRef
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetWorkoutMetricsBatch записывает метрики нагрузки пачкой в одной транзакции.
// Пользовательские поля (name, trainer_override) и сами данные тренировки
// не трогает — только метрики.
func SetWorkoutMetricsBatch(db *sql.DB, works []*Workout) error {
	if len(works) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range works {
		if _, err := tx.Exec(`UPDATE workouts SET
		    if=?, tss=?, vi=?, avg_power_active=?, coasting_sec=?, no_power_sec=?,
		    suspect_dropout_sec=?, elapsed_sec=?, pause_sec=?, work_kj=?, metrics_version=?
		    WHERE id = ?`,
			nullableFloat(w.IF), nullableFloat(w.TSS), nullableFloat(w.VI),
			nullableInt(w.AvgPowerActive), nullableInt(w.CoastingSec), nullableInt(w.NoPowerSec),
			nullableInt(w.SuspectDropoutSec), nullableInt(w.ElapsedSec), nullableInt(w.PauseSec),
			nullableFloat(w.WorkKJ), w.MetricsVersion, w.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetNormPowers записывает NP и версии пачкой в одной транзакции: бэкфилл
// не должен оставлять базу в промежуточном состоянии.
func SetNormPowers(db *sql.DB, updates []NormPowerUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range updates {
		if _, err := tx.Exec(`UPDATE workouts SET norm_power = ?, np_version = ? WHERE id = ?`,
			u.NP, u.Version, u.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// LatestWorkout возвращает последнюю по дате старта тренировку.
func LatestWorkout(db *sql.DB) (*Workout, error) {
	var id int64
	if err := db.QueryRow(`SELECT id FROM workouts ORDER BY start_time DESC LIMIT 1`).Scan(&id); err != nil {
		return nil, err
	}
	return GetWorkout(db, id)
}

// ListWorkouts возвращает все тренировки от новых к старым.
func ListWorkouts(db *sql.DB) ([]Workout, error) {
	rows, err := db.Query(`SELECT id FROM workouts ORDER BY start_time DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workout
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		w, err := GetWorkout(db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func GetWorkout(db *sql.DB, id int64) (*Workout, error) {
	var w Workout
	var start string
	var durSec int64
	var dist sql.NullInt64
	var spd, mspd sql.NullFloat64
	var cad, mcad, ap, mp, np, hr, mhr, kcal, asc, desc, ftp sql.NullInt64
	var wkg sql.NullFloat64
	var ifv, tss, vi, work sql.NullFloat64
	var apa, coast, nop, susp, elapsed, pause sql.NullInt64
	var tr, gps int
	var ov sql.NullInt64
	err := db.QueryRow(`SELECT id, start_time, name, subsport, duration_sec, distance_m, has_gps, trainer,
	    trainer_override, avg_speed_ms, max_speed_ms, avg_cadence, max_cadence, avg_power, max_power,
	    norm_power, np_version, avg_hr, max_hr, calories, total_ascent, total_descent, ftp_w, weight_kg,
	    if, tss, vi, avg_power_active, coasting_sec, no_power_sec, suspect_dropout_sec,
	    elapsed_sec, pause_sec, work_kj, metrics_version
	    FROM workouts WHERE id = ?`, id).Scan(
		&w.ID, &start, &w.Name, &w.SubSport, &durSec, &dist, &gps, &tr, &ov,
		&spd, &mspd, &cad, &mcad, &ap, &mp, &np, &w.NPVersion, &hr, &mhr, &kcal, &asc, &desc, &ftp, &wkg,
		&ifv, &tss, &vi, &apa, &coast, &nop, &susp, &elapsed, &pause, &work, &w.MetricsVersion)
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
	w.IF = ifv.Float64
	w.TSS = tss.Float64
	w.VI = vi.Float64
	w.AvgPowerActive = int(apa.Int64)
	w.CoastingSec = int(coast.Int64)
	w.NoPowerSec = int(nop.Int64)
	w.SuspectDropoutSec = int(susp.Int64)
	w.ElapsedSec = int(elapsed.Int64)
	w.PauseSec = int(pause.Int64)
	w.WorkKJ = work.Float64
	return &w, nil
}

// CountSamples — число сэмплов тренировки. Нужно для проверки потолка ?raw=1
// и для расчёта размера бакета в Samples.
func CountSamples(db *sql.DB, id int64) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE workout_id = ?`, id).Scan(&n)
	return n, err
}

// Samples возвращает сэмплы, уменьшая количество до maxN точек бакет-усреднением
// (а не выборкой каждой N-й — иначе графики страдают от алиасинга).
// Точек возвращается не больше maxN: бакет = ceil(total/maxN), поэтому число
// групп ceil(total/бакет) ≤ maxN. При maxN >= количества сэмплов — все как есть.
func Samples(db *sql.DB, id int64, maxN int) ([]Sample, error) {
	if maxN < 1 {
		maxN = 1 // иначе деление на ноль при расчёте бакета
	}
	total, err := CountSamples(db, id)
	if err != nil {
		return nil, err
	}
	if total == 0 {
		return []Sample{}, nil
	}
	bucket := (total + maxN - 1) / maxN
	// Группируем по номеру записи, а не по ts_offset: иначе одинаковые offset
	// (запись 2 Гц) склеиваются и точек получается больше, чем просили.
	// Offset точки — средний по записям бакета: MIN прижимал бы точку к началу
	// бакета, и после длинной паузы внутри бакета она уезжала бы к её краю.
	rows, err := db.Query(`SELECT CAST(ROUND(AVG(ts_offset)) AS INTEGER), AVG(lat), AVG(lon),
	    CAST(AVG(power) AS INTEGER), CAST(AVG(cadence) AS INTEGER), CAST(AVG(hr) AS INTEGER),
	    AVG(speed_ms), AVG(altitude_m), MAX(distance_m)
	    FROM (
	      SELECT ts_offset, lat, lon, power, cadence, hr, speed_ms, altitude_m, distance_m,
	             ROW_NUMBER() OVER (ORDER BY ts_offset, rowid) - 1 AS rn
	      FROM samples WHERE workout_id = ?
	    ) GROUP BY rn / ? ORDER BY 1`, id, bucket)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSamples(rows)
}

// RawSamples возвращает все сэмплы тренировки как есть, без бакет-усреднения:
// для внешнего анализа нужны честные медиана и пики, а не средние по бакетам.
func RawSamples(db *sql.DB, id int64) ([]Sample, error) {
	rows, err := db.Query(`SELECT ts_offset, lat, lon, power, cadence, hr,
	    speed_ms, altitude_m, distance_m
	    FROM samples WHERE workout_id = ? ORDER BY ts_offset, rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSamples(rows)
}

// scanSamples разбирает строки сэмплов; набор и порядок колонок у Samples
// и RawSamples одинаковые.
func scanSamples(rows *sql.Rows) ([]Sample, error) {
	out := []Sample{}
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
