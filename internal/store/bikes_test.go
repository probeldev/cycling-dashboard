package store

import (
	"path/filepath"
	"testing"
	"time"
)

// TestBikesSeedAndAssign — миграция сидит велосипеды идемпотентно; назначение,
// смена и сброс bike_id меняют тренировку.
func TestBikesSeedAndAssign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db := openTestDBAt(t, path)

	bikes, err := Bikes(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(bikes) != 3 {
		t.Fatalf("%d велосипедов, хочу 3: %+v", len(bikes), bikes)
	}
	byName := map[string]Bike{}
	for _, b := range bikes {
		byName[b.Name] = b
	}
	for _, name := range []string{"Trek SL 1000", "Октопус", "Trek 820"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("нет велосипеда %q", name)
		}
	}
	if byName["Trek SL 1000"].Kind != "road" || byName["Октопус"].Kind != "fixed" {
		t.Errorf("типы: %+v", byName)
	}

	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: time.Hour},
		"hash-bike-1", "/tmp/b.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := GetWorkout(db, id); w.BikeID != 0 || w.Bike != "" {
		t.Errorf("по умолчанию велосипед не пуст: %+v", w)
	}

	trek := byName["Trek SL 1000"].ID
	if err := SetWorkoutBike(db, id, &trek); err != nil {
		t.Fatal(err)
	}
	w, err := GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if w.BikeID != trek || w.Bike != "Trek SL 1000" {
		t.Errorf("после назначения: BikeID=%d Bike=%q", w.BikeID, w.Bike)
	}

	okt := byName["Октопус"].ID
	if err := SetWorkoutBike(db, id, &okt); err != nil {
		t.Fatal(err)
	}
	if w, _ := GetWorkout(db, id); w.Bike != "Октопус" {
		t.Errorf("смена велосипеда: %q", w.Bike)
	}

	if err := SetWorkoutBike(db, id, nil); err != nil {
		t.Fatal(err)
	}
	if w, _ := GetWorkout(db, id); w.BikeID != 0 || w.Bike != "" {
		t.Errorf("сброс: BikeID=%d Bike=%q", w.BikeID, w.Bike)
	}

	// повторное открытие не дублирует сид
	db.Close()
	reopened := openTestDBAt(t, path)
	if again, err := Bikes(reopened); err != nil || len(again) != 3 {
		t.Errorf("после переоткрытия: %d, %v", len(again), err)
	}
}

// TestBikesIncludeInactive — деактивированный велосипед остаётся в списке,
// иначе деталка потеряет уже назначенную раму при сохранении.
func TestBikesIncludeInactive(t *testing.T) {
	db := openTestDB(t)
	bikes, err := Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	id := bikes[0].ID
	if _, err := db.Exec(`UPDATE bikes SET active = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	all, err := Bikes(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(bikes) {
		t.Fatalf("%d велосипедов, хочу %d (включая неактивный)", len(all), len(bikes))
	}
	found := false
	for _, b := range all {
		if b.ID == id {
			found = true
			if b.Active {
				t.Errorf("велосипед %d должен быть неактивным", id)
			}
		}
	}
	if !found {
		t.Errorf("деактивированный велосипед %d пропал из списка", id)
	}
	if all[0].ID == id {
		t.Errorf("неактивный велосипед должен быть после активных")
	}
}

// TestBikeStats — километры/часы/поездки по велосипедам.
func TestBikeStats(t *testing.T) {
	db := openTestDB(t)
	bikes, err := Bikes(db)
	if err != nil {
		t.Fatal(err)
	}
	trek := bikes[0].ID
	for _, w := range []struct {
		hash string
		km   float64
		hrs  float64
	}{
		{"hash-bs-1", 30, 2}, {"hash-bs-2", 20, 1},
	} {
		id, err := InsertWorkout(db, &Workout{
			StartTime: time.Now(), Duration: time.Duration(w.hrs * float64(time.Hour)), DistanceKm: w.km,
		}, w.hash, "/tmp/x.fit", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetWorkoutBike(db, id, &trek); err != nil {
			t.Fatal(err)
		}
	}
	// тренировка без велосипеда в статистику не попадает
	if _, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: time.Hour, DistanceKm: 10},
		"hash-bs-3", "/tmp/x.fit", 1); err != nil {
		t.Fatal(err)
	}

	stats, err := BikeStats(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("%d строк, хочу 1: %+v", len(stats), stats)
	}
	s := stats[0]
	if s.Rides != 2 || s.KM != 50 || s.Hours != 3 {
		t.Errorf("стат = %+v, хочу 2/50/3", s)
	}
}
