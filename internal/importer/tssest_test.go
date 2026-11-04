package importer

import (
	"database/sql"
	"math"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// trainingRide вставляет тренировку с мощностью, пульсом и TSS для обучения модели.
func trainingRide(t *testing.T, db *sql.DB, start time.Time, hr, tss int) int64 {
	t.Helper()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: time.Hour, AvgPower: 200, AvgHR: hr, TSS: float64(tss),
	}, "hash-train-"+start.Format(time.RFC3339)+"-"+t.Name(), "/nonexistent/train.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// hrOnlyRide вставляет тренировку без мощности, но с пульсом и длительностью.
func hrOnlyRide(t *testing.T, db *sql.DB, start time.Time, hr int) int64 {
	t.Helper()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: time.Hour, AvgHR: hr,
	}, "hash-hr-"+start.Format(time.RFC3339)+"-"+t.Name(), "/nonexistent/hr.fit", 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBackfillTSSEstimatesHROnly(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }

	// 5 обучающих тренировок: TSS/час ≈ пульс
	for d, hr := range []int{130, 140, 150, 160, 170} {
		trainingRide(t, db, day(d+1), hr, hr)
	}
	cand := hrOnlyRide(t, db, day(10), 155)

	n, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("оценено %d, хочу 1", n)
	}
	w, err := store.GetWorkout(db, cand)
	if err != nil {
		t.Fatal(err)
	}
	if !w.HasTSSEst {
		t.Fatal("HasTSSEst должен быть true")
	}
	if math.Abs(w.TSSEst-155) > 5 {
		t.Errorf("TSSEst = %v, хочу около 155", w.TSSEst)
	}
	if w.TSSEstLow {
		t.Error("HR внутри диапазона обучения: lowConfidence=false")
	}
}

func TestBackfillTSSEstTooFewTrainings(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	for d, hr := range []int{130, 160} {
		trainingRide(t, db, day(d+1), hr, hr)
	}
	hrOnlyRide(t, db, day(10), 155)

	n, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("оценено %d, хочу 0 (мало тренировок, < 3)", n)
	}
}

// TestBackfillTSSEstClosestAround — обучающий набор берётся из ближайших
// по времени тренировок с обеих сторон: поездка в середине истории получает
// оценку, даже если часть тренировок с мощностью позже неё.
func TestBackfillTSSEstClosestAround(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }

	// 2 обучающие ДО и 2 ПОСЛЕ кандидата: TSS/час ≈ пульс
	trainingRide(t, db, day(1), 130, 130)
	trainingRide(t, db, day(2), 140, 140)
	trainingRide(t, db, day(20), 160, 160)
	trainingRide(t, db, day(21), 170, 170)
	cand := hrOnlyRide(t, db, day(10), 155)

	n, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("оценено %d, хочу 1 (ближайшие до и после)", n)
	}
	w, err := store.GetWorkout(db, cand)
	if err != nil {
		t.Fatal(err)
	}
	if !w.HasTSSEst {
		t.Fatal("HasTSSEst должен быть true")
	}
	if math.Abs(w.TSSEst-155) > 15 {
		t.Errorf("TSSEst = %v, хочу около 155", w.TSSEst)
	}
	// обучающих 4 (< 5): оценка помечается как менее надёжная
	if !w.TSSEstLow {
		t.Error("n=4 < 5: lowConfidence должен быть true")
	}
}

func TestBackfillTSSEstLowCoverage(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	for d, hr := range []int{130, 140, 150, 160, 170} {
		trainingRide(t, db, day(d+1), hr, hr)
	}
	cand := hrOnlyRide(t, db, day(10), 155)

	// 10 сэмплов, только 3 с пульсом — покрытие 0.3
	samples := make([]store.Sample, 10)
	for i := range samples {
		samples[i] = store.Sample{Offset: int64(i)}
		if i < 3 {
			samples[i].HR = 155
		}
	}
	if err := store.InsertSamples(db, cand, samples); err != nil {
		t.Fatal(err)
	}

	n, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("оценено %d, хочу 0 (низкое покрытие пульса)", n)
	}
	w, err := store.GetWorkout(db, cand)
	if err != nil {
		t.Fatal(err)
	}
	if w.HasTSSEst {
		t.Error("оценка не должна появиться при покрытии < 0.5")
	}
}

func TestBackfillTSSEstHygiene(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	for d, hr := range []int{130, 140, 150, 160, 170} {
		trainingRide(t, db, day(d+1), hr, hr)
	}
	cand := hrOnlyRide(t, db, day(10), 155)

	if n, err := BackfillTSSEst(db); err != nil || n != 1 {
		t.Fatalf("первый бэкфилл: %d, %v; хочу 1", n, err)
	}
	// появились данные мощности: tss не NULL — оценка должна очиститься
	if _, err := db.Exec(`UPDATE workouts SET tss = 120, avg_power = 200 WHERE id = ?`, cand); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillTSSEst(db); err != nil || n != 0 {
		t.Fatalf("гигиена: %d, %v; хочу 0", n, err)
	}
	w, err := store.GetWorkout(db, cand)
	if err != nil {
		t.Fatal(err)
	}
	if w.HasTSSEst {
		t.Error("tss_est должен очиститься после появления tss")
	}
	if w.TSSEstLow {
		t.Error("tss_est_low должен сброситься в 0")
	}
}

func TestBackfillTSSEstIdempotent(t *testing.T) {
	db := openTestDB(t)
	day := func(d int) time.Time { return time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC) }
	for d, hr := range []int{130, 140, 150, 160, 170} {
		trainingRide(t, db, day(d+1), hr, hr)
	}
	hrOnlyRide(t, db, day(10), 155)

	n1, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Fatalf("первый прогон: %d, хочу 1", n1)
	}
	n2, err := BackfillTSSEst(db)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Fatalf("второй прогон: %d, хочу 0", n2)
	}
}
