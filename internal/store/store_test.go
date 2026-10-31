package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insertWorkout — тренировка с n сэмплами по 1 с; power задаёт мощность записи.
func insertWorkout(t *testing.T, db *sql.DB, n int, power func(i int) int) int64 {
	t.Helper()
	id, err := InsertWorkout(db, &Workout{
		StartTime: time.Now(), Duration: time.Duration(n) * time.Second,
	}, "hash-"+t.Name(), filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]Sample, n)
	for i := range samples {
		samples[i] = Sample{Offset: int64(i), Power: power(i), HasPwr: true}
	}
	if err := InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSamplesBound(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 10, func(i int) int { return i })

	if got, err := CountSamples(db, id); err != nil || got != 10 {
		t.Fatalf("CountSamples = %d, %v; хочу 10, nil", got, err)
	}
	for _, tt := range []struct{ max, want int }{
		{1, 1}, {3, 3}, {10, 10}, {100, 10},
	} {
		got, err := Samples(db, id, tt.max)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tt.want {
			t.Errorf("max=%d: %d точек, хочу %d", tt.max, len(got), tt.want)
		}
	}

	// усреднение по бакету, а не выборка каждой N-й: среднее 0..9 = 4.5 → 4
	one, err := Samples(db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Power != 4 {
		t.Errorf("средняя по бакету = %d Вт, хочу 4 (AVG 0..9)", one[0].Power)
	}

	// maxN <= 0 не должен ронять делением на ноль
	if got, err := Samples(db, id, 0); err != nil || len(got) != 1 {
		t.Errorf("maxN=0: %d точек, %v; хочу 1 точку", len(got), err)
	}
}

func TestRawSamplesKeepsPeaks(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 100, func(i int) int {
		if i == 50 {
			return 900
		}
		return 100
	})

	raw, err := RawSamples(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 100 {
		t.Fatalf("%d точек, хочу 100", len(raw))
	}
	if raw[50].Power != 900 {
		t.Errorf("пик = %d Вт, хочу 900", raw[50].Power)
	}

	bucketed, err := Samples(db, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	peak := 0
	for _, s := range bucketed {
		peak = max(peak, s.Power)
	}
	if peak >= 900 {
		t.Errorf("при усреднении пик не размазался: %d Вт", peak)
	}
}

func TestRawSamplesNulls(t *testing.T) {
	db := openTestDB(t)
	id := insertWorkout(t, db, 1, func(int) int { return 0 })
	// вторая запись — полная дыра: все датчики отвалились
	if err := InsertSamples(db, id, []Sample{{Offset: 5}}); err != nil {
		t.Fatal(err)
	}

	raw, err := RawSamples(db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("%d точек, хочу 2", len(raw))
	}
	if !raw[0].HasPwr || raw[0].Power != 0 {
		t.Errorf("честный ноль потерялся: %+v", raw[0])
	}
	if raw[1].HasPwr || raw[1].HasCad || raw[1].HasPos || raw[1].HasAlt || raw[1].HasDist {
		t.Errorf("дыра превратилась в данные: %+v", raw[1])
	}
}

func TestSamplesEmptyWorkout(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now()},
		"hash-empty", filepath.Join(t.TempDir(), "empty.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Samples(db, id, 500); err != nil || got == nil || len(got) != 0 {
		t.Errorf("Samples без сэмплов: %v, %v; хочу пустой (не nil) срез", got, err)
	}
	if got, err := RawSamples(db, id); err != nil || got == nil || len(got) != 0 {
		t.Errorf("RawSamples без сэмплов: %v, %v; хочу пустой (не nil) срез", got, err)
	}
}

// TestSamplesBucketOffsetIsMean — offset точки — среднее по записям бакета,
// а не MIN: иначе после длинной паузы внутри бакета точка садится на его край.
func TestSamplesBucketOffsetIsMean(t *testing.T) {
	db := openTestDB(t)
	id, err := InsertWorkout(db, &Workout{StartTime: time.Now(), Duration: 105 * time.Second},
		"hash-bucket-mean", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	for i := 0; i < 5; i++ { // до паузы
		samples = append(samples, Sample{Offset: int64(i), Power: 100, HasPwr: true})
	}
	for i := 0; i < 5; i++ { // после паузы в 96 с
		samples = append(samples, Sample{Offset: int64(100 + i), Power: 100, HasPwr: true})
	}
	if err := InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}

	one, err := Samples(db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("%d точек, хочу 1", len(one))
	}
	// среднее (0+1+2+3+4+100+101+102+103+104)/10 = 52
	if one[0].Offset != 52 {
		t.Errorf("offset точки = %d, хочу 52 (среднее по бакету, а не MIN)", one[0].Offset)
	}
}
