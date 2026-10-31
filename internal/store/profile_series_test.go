package store

import (
	"math"
	"testing"
	"time"
)

func profileNear(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, хочу %v", name, got, want)
	}
}

// TestProfileSeriesSteps — значение меняется только при новой записи, а до
// первой записи вида — null. Даты — локальные календарные дни.
func TestProfileSeriesSteps(t *testing.T) {
	loc := withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	at := func(day int, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, loc) }

	// 23:30 и 00:30 — проверяем, что день берётся локальный, а не UTC
	if err := AddProfileEntry(db, "weight", 75, at(1, 23)); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 156, at(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 170, at(5, 12)); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp_outdoor", 180, at(7, 12)); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 10, 3, 15, 0, 0, 0, loc)
	to := time.Date(2026, 10, 8, 9, 0, 0, 0, loc)
	points, err := ProfileSeries(db, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 6 {
		t.Fatalf("%d точек, хочу 6 (3..8 октября)", len(points))
	}
	for i, p := range points {
		want := time.Date(2026, 10, 3+i, 0, 0, 0, 0, loc)
		if !p.Date.Equal(want) {
			t.Errorf("точка %d: дата %v, хочу %v", i, p.Date, want)
		}
		profileNear(t, "weight", p.Weight, 75)
	}

	// 3–4 октября: FTP 156, 5–6: 170, с 7-го — уличный FTP, станковый не меняется
	for _, i := range []int{0, 1} {
		profileNear(t, "ftpTrainer", points[i].FTPTrainer, 156)
		profileNear(t, "wPerKgTrainer", points[i].WPerKgTrainer, 156.0/75)
		if points[i].FTPOutdoor != nil || points[i].WPerKgOutdoor != nil {
			t.Errorf("уличный FTP раньше записи: %+v", points[i])
		}
	}
	for _, i := range []int{2, 3, 4, 5} {
		profileNear(t, "ftpTrainer", points[i].FTPTrainer, 170)
		profileNear(t, "wPerKgTrainer", points[i].WPerKgTrainer, 170.0/75)
	}
	if points[2].FTPOutdoor != nil {
		t.Errorf("уличный FTP появился до 7 октября: %+v", points[2])
	}
	for _, i := range []int{4, 5} {
		profileNear(t, "ftpOutdoor", points[i].FTPOutdoor, 180)
		profileNear(t, "wPerKgOutdoor", points[i].WPerKgOutdoor, 180.0/75)
	}
}

// TestProfileSeriesEmpty — пустая история даёт пустой массив, а не ряд null.
func TestProfileSeriesEmpty(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	points, err := ProfileSeries(db, dayOf(t, 1), dayOf(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if points == nil || len(points) != 0 {
		t.Errorf("пустая история: %#v, хочу пустой массив", points)
	}
}

// TestProfileSeriesZeroWeight — нулевой вес не даёт деления на ноль: Вт/кг null.
func TestProfileSeriesZeroWeight(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	if err := AddProfileEntry(db, "weight", 0, dayOf(t, 1)); err != nil {
		t.Fatal(err)
	}
	if err := AddProfileEntry(db, "ftp", 200, dayOf(t, 1)); err != nil {
		t.Fatal(err)
	}
	points, err := ProfileSeries(db, dayOf(t, 1), dayOf(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("%d точек, хочу 1", len(points))
	}
	profileNear(t, "weight", points[0].Weight, 0)
	profileNear(t, "ftpTrainer", points[0].FTPTrainer, 200)
	if points[0].WPerKgTrainer != nil {
		t.Errorf("Вт/кг при весе 0 = %v, хочу null", *points[0].WPerKgTrainer)
	}
}

// TestProfileSeriesStartsAtFirstEntry — окно шире истории: ряд начинается с
// первой записи, а не с from, и не тянет значение назад во времени.
func TestProfileSeriesStartsAtFirstEntry(t *testing.T) {
	withLocal(t, "Europe/Minsk")
	db := openTestDB(t)
	if err := AddProfileEntry(db, "weight", 70, dayOf(t, 10)); err != nil {
		t.Fatal(err)
	}
	points, err := ProfileSeries(db, dayOf(t, 1), dayOf(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 {
		t.Fatalf("%d точек, хочу 3 (10..12)", len(points))
	}
	if !points[0].Date.Equal(StartOfDay(dayOf(t, 10))) {
		t.Errorf("ряд начался с %v, хочу 10 октября", points[0].Date)
	}
	if points[0].FTPTrainer != nil || points[0].WPerKgTrainer != nil {
		t.Errorf("FTP до записи не null: %+v", points[0])
	}
}
