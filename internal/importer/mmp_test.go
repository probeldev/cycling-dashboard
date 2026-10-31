package importer

import (
	"math"
	"os"
	"path/filepath"
	"sort"
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
	// накат входит временем, дропауты — fill-forward (как NP)
	want := map[int]float64{
		1: 436, 2: 395, 5: 376.6, 10: 367.6, 20: 323.6, 30: 251.4,
		60: 218.1, 120: 185.7, 300: 169.4, 600: 160.3, 900: 156.3,
		1200: 153.7, 1800: 122.0,
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

// TestMMPCoasting — накат (power == 0) идёт временем: окно 120 с не «склеивает»
// два усилия по 60 с.
func TestMMPCoasting(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 300, HasPwr: true})
	}
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(60 + i), Power: 0, HasPwr: true})
	}
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(120 + i), Power: 300, HasPwr: true})
	}
	m := mmpOf(samples)
	if got := m[60]; math.Abs(got-300) > 1e-9 {
		t.Errorf("MMP 60 = %v, хочу 300", got)
	}
	if got := m[120]; math.Abs(got-150) > 1e-9 {
		t.Errorf("MMP 120 = %v, хочу 150 (с накатом)", got)
	}
}

// TestMMPFillForward — дропаут заполняется последним валидным значением:
// 10 с 200 + 10 с дропаута + 10 с 200 → MMP 30 = 200.
func TestMMPFillForward(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 10; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true})
	}
	for i := 0; i < 10; i++ {
		samples = append(samples, store.Sample{Offset: int64(10 + i)}) // дропаут
	}
	for i := 0; i < 10; i++ {
		samples = append(samples, store.Sample{Offset: int64(20 + i), Power: 200, HasPwr: true})
	}
	m := mmpOf(samples)
	if got := m[30]; math.Abs(got-200) > 1e-9 {
		t.Errorf("MMP 30 = %v, хочу 200 (дропаут заполняется последним значением)", got)
	}
}

// TestMMPMonotonic — MMP не растёт с длиной окна: MMP(w) >= MMP(2w).
func TestMMPMonotonic(t *testing.T) {
	check := func(t *testing.T, m map[int]float64) {
		t.Helper()
		// все пары окон: более короткое окно не может быть слабее длинного
		windows := make([]int, 0, len(m))
		for w := range m {
			windows = append(windows, w)
		}
		sort.Ints(windows)
		for i := 0; i < len(windows); i++ {
			for j := i + 1; j < len(windows); j++ {
				if m[windows[i]]+1e-9 < m[windows[j]] {
					t.Errorf("MMP(%d)=%v < MMP(%d)=%v", windows[i], m[windows[i]], windows[j], m[windows[j]])
				}
			}
		}
	}

	// синтетика: 308-подобный рамп с провалами 20% (заполняются fill-forward)
	var ramp []store.Sample
	for i := 0; i < 3000; i++ {
		s := store.Sample{Offset: int64(i), Power: 100 + i/10}
		if i%50 < 40 {
			s.HasPwr = true
		}
		ramp = append(ramp, s)
	}
	rm := mmpOf(ramp)
	check(t, rm)
	// эталон синтетического рампа (посчитан независимым скриптом)
	for w, want := range map[int]float64{
		1: 398, 5: 398, 30: 397.7, 60: 396.2, 120: 393.3, 300: 384.3,
		600: 369.3, 900: 354.3, 1200: 339.3, 1800: 309.3,
	} {
		if rm[w] != want {
			t.Errorf("рамп: MMP %d = %v, хочу %v", w, rm[w], want)
		}
	}

	// реальный 308, если файл есть
	if _, err := statFile("../../tracks/20261029204857.fit"); err == nil {
		_, samples, err := ParseFile("../../tracks/20261029204857.fit")
		if err != nil {
			t.Fatal(err)
		}
		m := mmpOf(samples)
		check(t, m)
		if m[1200] != 98.3 || m[1800] != 97.2 || m[2700] != 97.1 {
			t.Errorf("308: MMP 1200=%v 1800=%v 2700=%v, хочу 98.3/97.2/97.1", m[1200], m[1800], m[2700])
		}
	}
}

// TestMMPFractionalWindow — запись, пересекающая правую границу, входит частично.
func TestMMPFractionalWindow(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true},  // 10 с
		{Offset: 10, Power: 100, HasPwr: true}, // 12 с
		{Offset: 22, Power: 50, HasPwr: true},  // 1 с
	}
	// окно 20 с: 10 с по 200 + 10 с по 100 (вторая запись входит частично)
	if got := mmpOf(samples)[20]; math.Abs(got-150) > 0.05 {
		t.Errorf("MMP 20 = %v, хочу 150 (частичная запись на границе)", got)
	}
}

// TestMMPLongRecordInsideWindow — длинная запись внутри окна не раздувает
// короткое окно: 10 с 300 Вт + 100 с нуля → окно 30 с = 100 Вт.
func TestMMPLongRecordInsideWindow(t *testing.T) {
	samples := []store.Sample{{Offset: 0, Power: 300, HasPwr: true}} // запись 10 с
	for i := 0; i < 100; i++ {                                       // дальше 100 с нуля
		samples = append(samples, store.Sample{Offset: int64(10 + i), Power: 0, HasPwr: true})
	}
	m := mmpOf(samples)
	if got := m[30]; math.Abs(got-100) > 1e-9 {
		t.Errorf("MMP 30 = %v, хочу 100 (10 с по 300 + 20 с нуля)", got)
	}
	// окно 5 с не может начинаться с 10-секундной записи
	if got := m[5]; math.Abs(got-0) > 1e-9 {
		t.Errorf("MMP 5 = %v, хочу 0 (10-секундная запись не представляет 5 с)", got)
	}
}

// TestMMPShortWindow — известное ограничение: запись длиннее окна не может
// начинать окно (иначе «1-секундный пик» стал бы средним за 60 с). Тест
// фиксирует поведение, чтобы его не потеряли при будущих правках.
func TestMMPShortWindow(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 300, HasPwr: true},  // длительность 60 с
		{Offset: 60, Power: 100, HasPwr: true}, // длительность 1 с
	}
	m := mmpOf(samples)
	if got := m[1]; math.Abs(got-100) > 1e-9 {
		t.Errorf("MMP 1 = %v, хочу 100: запись 60 с не может быть 1-секундным окном", got)
	}
}

// TestReimportResetsMMP — повторная запись тренировки сбрасывает версию и
// удаляет старые рекорды; бэкфилл восстанавливает их по новым сэмплам.
func TestReimportResetsMMP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reset.fit")
	writeFIT(t, path, fit.SubSportRoad, 30)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if recs, err := store.WorkoutMMP(db, w.ID); err != nil || len(recs) == 0 {
		t.Fatalf("исходные записи: %v, %v", recs, err)
	}
	var hash, filePath string
	if err := db.QueryRow(`SELECT file_hash, file_path FROM workouts WHERE id = ?`, w.ID).Scan(&hash, &filePath); err != nil {
		t.Fatal(err)
	}

	// upsert той же тренировки (как при импорте) сбрасывает MMP и рекорды
	id, err := store.InsertWorkout(db, w, hash, filePath, 1)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT mmp_version FROM workouts WHERE id = ?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Errorf("mmp_version = %d, хочу 0 после повторной записи", version)
	}
	if recs, err := store.WorkoutMMP(db, id); err != nil || len(recs) != 0 {
		t.Errorf("старые рекорды не удалены: %v, %v", recs, err)
	}

	// досборка: сэмплы + бэкфилл возвращают рекорды
	_, samples, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	if n, err := BackfillMMP(db); err != nil || n != 1 {
		t.Fatalf("бэкфилл MMP: %d, %v; хочу 1", n, err)
	}
	if recs, err := store.WorkoutMMP(db, id); err != nil || len(recs) == 0 {
		t.Errorf("бэкфилл не восстановил рекорды: %v, %v", recs, err)
	}
}

// TestImportAtomicOnSampleError — сбой при вставке сэмплов откатывает всю
// транзакцию тренировки: старые MMP/зоны остаются согласованными.
func TestImportAtomicOnSampleError(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "atomic.fit"), fit.SubSportRoad, 30)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_samples BEFORE INSERT ON samples
	    BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	zonesBefore, err := store.WorkoutZones(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportDir(db, dir, true); err == nil {
		t.Fatal("жду ошибку от триггера")
	}
	// состояние прежнее: зоны и рекорды на месте, версия актуальна
	if zonesAfter, err := store.WorkoutZones(db, w.ID); err != nil || zonesAfter != zonesBefore {
		t.Errorf("зоны изменились после отката: %v -> %v, %v", zonesBefore, zonesAfter, err)
	}
	var version int
	if err := db.QueryRow(`SELECT mmp_version FROM workouts WHERE id = ?`, w.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != mmpVersion {
		t.Errorf("mmp_version = %d, хочу %d (транзакция должна откатиться)", version, mmpVersion)
	}
	recs, err := store.WorkoutMMP(db, w.ID)
	if err != nil || len(recs) == 0 {
		t.Errorf("после отката рекорды пропали: %v, %v", recs, err)
	}
}

// TestReimportReplacesMMP — полный ImportDir(force) заменяет записи без дублей.
func TestReimportReplacesMMP(t *testing.T) {
	dir := t.TempDir()
	writeFIT(t, filepath.Join(dir, "reimport.fit"), fit.SubSportRoad, 30)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.WorkoutMMP(db, w.ID)
	if err != nil || len(before) == 0 {
		t.Fatalf("исходные записи: %v, %v", before, err)
	}
	if _, _, err := ImportDir(db, dir, true); err != nil {
		t.Fatal(err)
	}
	after, err := store.WorkoutMMP(db, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("окон %d после реимпорта, было %d", len(after), len(before))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("запись %d: %v -> %v", i, before[i], after[i])
		}
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM power_records WHERE workout_id = ?`, w.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != len(before) {
		t.Errorf("строк %d после реимпорта, было %d (дубли?)", rows, len(before))
	}
}
