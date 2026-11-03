package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestWorkoutContextStore — заметка сохраняется, пустое значение пишется
// как NULL.
func TestWorkoutContextStore(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: 90 * time.Minute},
		"hash-ctx-store", "/tmp/c.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	notes := "темповая, ветер"
	if err := SetWorkoutContext(db, id, WorkoutContextUpdate{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	w, err := GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.Notes != "темповая, ветер" {
		t.Errorf("Notes=%q", w.Notes)
	}

	// пустой апдейт ничего не трогает
	if err := SetWorkoutContext(db, id, WorkoutContextUpdate{}); err != nil {
		t.Fatal(err)
	}
	if w, _ := GetWorkout(db, id); w.Notes != "темповая, ветер" {
		t.Errorf("пустой апдейт стёр заметку: Notes=%q", w.Notes)
	}

	// очистка: пустая строка → NULL
	empty := ""
	if err := SetWorkoutContext(db, id, WorkoutContextUpdate{Notes: &empty}); err != nil {
		t.Fatal(err)
	}
	var sqlNotes sql.NullString
	if err := db.QueryRow(`SELECT notes FROM workouts WHERE id = ?`, id).Scan(&sqlNotes); err != nil {
		t.Fatal(err)
	}
	if sqlNotes.Valid {
		t.Errorf("после очистки не NULL: notes=%v", sqlNotes)
	}
	if w, _ := GetWorkout(db, id); w.Notes != "" {
		t.Errorf("после очистки: Notes=%q", w.Notes)
	}
}

// TestDropRemovedColumns — миграция удаляет колонки temperature, avg_temp и
// rpe вместе с триггерами из базы старой схемы, остальные данные целы.
// Повторный прогон безопасен (идемпотентность).
func TestDropRemovedColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// старая схема: колонки rpe/avg_temp/temperature и триггеры инварианта
	if _, err := raw.Exec(`
CREATE TABLE workouts (
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
    rpe           INTEGER,
    notes         TEXT,
    avg_temp      REAL,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE samples (
    workout_id  INTEGER NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    ts_offset   INTEGER NOT NULL,
    lat         REAL,
    lon         REAL,
    power       INTEGER,
    cadence     INTEGER,
    hr          INTEGER,
    speed_ms    REAL,
    altitude_m  REAL,
    distance_m  REAL,
    temperature REAL
);
CREATE TRIGGER workouts_rpe_insert
BEFORE INSERT ON workouts
WHEN NEW.rpe IS NOT NULL AND (NEW.rpe < 1 OR NEW.rpe > 10)
BEGIN
    SELECT RAISE(ABORT, 'rpe must be between 1 and 10');
END;
CREATE TRIGGER workouts_rpe_update
BEFORE UPDATE OF rpe ON workouts
WHEN NEW.rpe IS NOT NULL AND (NEW.rpe < 1 OR NEW.rpe > 10)
BEGIN
    SELECT RAISE(ABORT, 'rpe must be between 1 and 10');
END;`); err != nil {
		t.Fatal(err)
	}
	res, err := raw.Exec(`INSERT INTO workouts
	    (file_hash, file_path, file_mtime, start_time, year, month, has_gps, trainer,
	     duration_sec, distance_m, avg_power, calories, rpe, notes, avg_temp)
	    VALUES ('hash-old', '/tmp/old.fit', 1, '2026-10-01T10:00:00Z', 2026, 10, 1, 0,
	     3600, 30000, 200, 500, 7, 'старая заметка', 12.5)`)
	if err != nil {
		t.Fatal(err)
	}
	wid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO samples (workout_id, ts_offset, power, hr, temperature)
	    VALUES (?, 0, 200, 140, 12.0), (?, 1, 210, 141, 12.5)`, wid, wid); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, m := range []struct{ table, column string }{
		{"workouts", "rpe"},
		{"workouts", "avg_temp"},
		{"samples", "temperature"},
	} {
		has, err := hasColumn(db, m.table, m.column)
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Errorf("колонка %s.%s не удалена", m.table, m.column)
		}
	}
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
	    WHERE type = 'trigger' AND name LIKE 'workouts_rpe_%'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 {
		t.Errorf("триггеров rpe осталось: %d", triggers)
	}

	// данные выжили
	var dist, power, kcal int
	var notes string
	if err := db.QueryRow(`SELECT distance_m, avg_power, calories, COALESCE(notes, '')
	    FROM workouts WHERE id = ?`, wid).Scan(&dist, &power, &kcal, &notes); err != nil {
		t.Fatal(err)
	}
	if dist != 30000 || power != 200 || kcal != 500 || notes != "старая заметка" {
		t.Errorf("тренировка: dist=%d power=%d kcal=%d notes=%q", dist, power, kcal, notes)
	}
	var samples int
	if err := db.QueryRow(`SELECT COUNT(*) FROM samples WHERE workout_id = ?`, wid).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 2 {
		t.Errorf("сэмплов %d, хочу 2", samples)
	}

	// идемпотентность: повторная миграция не падает
	if err := migrate(db); err != nil {
		t.Errorf("повторная миграция: %v", err)
	}
}
