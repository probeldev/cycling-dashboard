package importer

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/tormoder/fit"

	"cycling-dashboard/internal/store"
)

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// TestMMPConstant — постоянные 200 Вт 10 мин: все окна до 600 с равны 200,
// более длинных окон нет.
func TestMMPConstant(t *testing.T) {
	samples := series(600, 1, func(int) int { return 200 })
	m := mmpOf(samples)
	for _, w := range []int{1, 2, 5, 10, 20, 30, 60, 120, 300, 600} {
		got, ok := m[w]
		if !ok || math.Abs(got-200) > 1e-9 {
			t.Errorf("окно %d: %v (есть=%v), хочу 200", w, got, ok)
		}
	}
	for _, w := range []int{900, 1200, 1800, 3600} {
		if _, ok := m[w]; ok {
			t.Errorf("окно %d не должно считаться при 600 с данных", w)
		}
	}
}

// TestMMPInterval — ступенька 300→100 Вт: 60 с = 300, 120 с = 200,
// 300 с — среднее по фактическим данным.
func TestMMPInterval(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 300, HasPwr: true})
	}
	for i := 0; i < 240; i++ {
		samples = append(samples, store.Sample{Offset: int64(60 + i), Power: 100, HasPwr: true})
	}
	m := mmpOf(samples)
	if got := m[60]; math.Abs(got-300) > 1e-9 {
		t.Errorf("MMP 60 = %v, хочу 300", got)
	}
	if got := m[120]; math.Abs(got-200) > 1e-9 {
		t.Errorf("MMP 120 = %v, хочу 200", got)
	}
	// 300 с: (60·300 + 240·100) / 300
	if got := m[300]; math.Abs(got-140) > 1e-9 {
		t.Errorf("MMP 300 = %v, хочу 140", got)
	}
}

// TestMMPPauseNotSmeared — пауза рвёт сегмент: окно 120 с не склеивает
// мощность до и после остановки.
func TestMMPPauseNotSmeared(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 300, HasPwr: true})
	}
	// граница разрыва: дистанция не растёт (30-секундная остановка)
	samples[59].DistanceM, samples[59].HasDist = 1000, true
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{
			Offset: int64(90 + i), Power: 100, HasPwr: true, DistanceM: 1000, HasDist: true,
		})
	}
	m := mmpOf(samples)
	if got := m[60]; math.Abs(got-300) > 1e-9 {
		t.Errorf("MMP 60 = %v, хочу 300 (лучший отрезок до паузы)", got)
	}
	if _, ok := m[120]; ok {
		t.Errorf("окно 120 склеило данные через паузу")
	}
}

// TestMMP418Golden — эталон на реальном треке (файл личный, без него тест
// пропускается). Значения посчитаны независимым скриптом по сэмплам.
func TestMMP418Golden(t *testing.T) {
	const path = "../../tracks/20261031105658.fit"
	if _, err := statFile(path); err != nil {
		t.Skip("нет tracks/20261031105658.fit")
	}
	_, samples, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]float64{
		1: 436, 2: 395, 5: 376.6, 10: 367.6, 20: 323.6, 30: 271.4,
		60: 218.1, 120: 200.2, 300: 184.9, 600: 176.4, 900: 170.9,
		1200: 166.4, 1800: 141.6,
	}
	m := mmpOf(samples)
	if len(m) != len(want) {
		t.Errorf("окон %d, хочу %d: %v", len(m), len(want), m)
	}
	for w, v := range want {
		if got := m[w]; math.Abs(got-v) > 0.05 {
			t.Errorf("MMP %d = %v, хочу %v", w, got, v)
		}
	}
}

// TestImportAndBackfillMMP — импорт пишет MMP, бэкфилл идемпотентен.
func TestImportAndBackfillMMP(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "mmp.fit"), fit.SubSportRoad, 30)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := store.WorkoutMMP(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 { // 1, 2, 5, 10, 20, 30 — ровно 30 с данных
		t.Errorf("окон %d, хочу 6: %v", len(recs), recs)
	}
	for _, r := range recs {
		if math.Abs(r.Watts-200) > 1e-9 {
			t.Errorf("окно %d: %v, хочу 200", r.WindowSec, r.Watts)
		}
	}

	// имитируем старую базу и прогоняем бэкфилл
	if _, err := db.Exec(`DELETE FROM power_records WHERE workout_id = ?`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workouts SET mmp_version = 0 WHERE id = ?`, w.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillMMP(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл MMP: %d, %v; хочу 1", n, err)
	}
	recs, err = store.WorkoutMMP(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 6 {
		t.Errorf("после бэкфилла окон %d, хочу 6", len(recs))
	}
	if n, err := BackfillMMP(db); err != nil || n != 0 {
		t.Errorf("повторный бэкфилл: %d, %v; хочу 0", n, err)
	}
}
