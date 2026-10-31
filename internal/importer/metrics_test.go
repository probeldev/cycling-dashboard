package importer

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tormoder/fit"

	"cycling-dashboard/internal/store"
)

func checkFinite(t *testing.T, vals ...float64) {
	t.Helper()
	for _, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("нефинитное значение: %v", v)
		}
	}
}

// TestApplyMetricsSynthetic — FTP=200, NP=200, час работы → IF=1, TSS=100.
func TestApplyMetricsSynthetic(t *testing.T) {
	samples := series(3600, 1, func(int) int { return 200 })
	w := &store.Workout{
		Duration: 3600 * time.Second, NormPower: 200, AvgPower: 200, FTP: 200, ElapsedSec: 3600,
	}
	applyMetrics(w, samples, 3600, 0)

	if math.Abs(w.IF-1) > 1e-9 {
		t.Errorf("IF = %v, хочу 1", w.IF)
	}
	if math.Abs(w.TSS-100) > 0.01 {
		t.Errorf("TSS = %v, хочу 100", w.TSS)
	}
	if math.Abs(w.VI-1) > 1e-9 {
		t.Errorf("VI = %v, хочу 1", w.VI)
	}
	if w.AvgPowerActive != 200 {
		t.Errorf("AvgPowerActive = %d, хочу 200", w.AvgPowerActive)
	}
	if w.CoastingSec != 0 || w.NoPowerSec != 0 || w.SuspectDropoutSec != 0 {
		t.Errorf("лишние секунды: coast=%d no_power=%d suspect=%d",
			w.CoastingSec, w.NoPowerSec, w.SuspectDropoutSec)
	}
	if math.Abs(w.WorkKJ-720) > 0.1 {
		t.Errorf("WorkKJ = %v, хочу 720", w.WorkKJ)
	}
	if w.PauseSec != 0 {
		t.Errorf("PauseSec = %d, хочу 0", w.PauseSec)
	}
	if w.MetricsVersion != metricsVersion {
		t.Errorf("MetricsVersion = %d, хочу %d", w.MetricsVersion, metricsVersion)
	}
	checkFinite(t, w.IF, w.TSS, w.VI, w.WorkKJ)
}

// TestApplyMetricsNoPower — без данных поверметра IF/TSS/VI не считаются,
// а время пропусков учитывается.
func TestApplyMetricsNoPower(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 10; i++ {
		samples = append(samples, store.Sample{Offset: int64(i)})
	}
	w := &store.Workout{Duration: 10 * time.Second, FTP: 156}
	applyMetrics(w, samples, 0, 0)

	if w.IF != 0 || w.TSS != 0 || w.VI != 0 {
		t.Errorf("без мощности метрики не NULL: IF=%v TSS=%v VI=%v", w.IF, w.TSS, w.VI)
	}
	if w.NoPowerSec != 10 {
		t.Errorf("NoPowerSec = %d, хочу 10", w.NoPowerSec)
	}
	checkFinite(t, w.IF, w.TSS, w.VI)
}

// TestApplyMetricsNoFTP — без FTP IF и TSS не считаются, VI считается: он от FTP
// не зависит (это разброс мощности, а не нагрузка).
func TestApplyMetricsNoFTP(t *testing.T) {
	samples := series(60, 1, func(int) int { return 200 })
	w := &store.Workout{Duration: 60 * time.Second, NormPower: 200, AvgPower: 200}
	applyMetrics(w, samples, 0, 0)

	if w.IF != 0 || w.TSS != 0 {
		t.Errorf("без FTP: IF=%v TSS=%v, хочу 0", w.IF, w.TSS)
	}
	if math.Abs(w.VI-1) > 1e-9 {
		t.Errorf("VI = %v, хочу 1 (не зависит от FTP)", w.VI)
	}
	checkFinite(t, w.IF, w.TSS, w.VI)
}

// TestApplyMetricsZeroAvgPower — нулевая средняя мощность: VI не считается.
func TestApplyMetricsZeroAvgPower(t *testing.T) {
	w := &store.Workout{Duration: time.Minute, NormPower: 140, FTP: 156}
	applyMetrics(w, nil, 0, 0)
	if w.VI != 0 {
		t.Errorf("VI = %v, хочу 0 при avg_power=0", w.VI)
	}
	checkFinite(t, w.IF, w.TSS, w.VI)
}

// TestApplyMetricsPauseClamp — elapsed меньше duration: паузы не отрицательные.
func TestApplyMetricsPauseClamp(t *testing.T) {
	w := &store.Workout{Duration: 100 * time.Second, ElapsedSec: 90}
	applyMetrics(w, nil, 90, 0)
	if w.PauseSec != 0 {
		t.Errorf("PauseSec = %d, хочу 0", w.PauseSec)
	}
}

// TestMetrics418Golden — эталон на реальном треке (файл личный и в git не лежит,
// поэтому без него тест пропускается).
func TestMetrics418Golden(t *testing.T) {
	const path = "../../tracks/20261031105658.fit"
	if _, err := os.Stat(path); err != nil {
		t.Skip("нет tracks/20261031105658.fit")
	}
	w, samples, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w.FTP = 156
	applyMetrics(w, samples, w.ElapsedSec, w.WorkKJ)

	checks := []struct {
		name string
		got  float64
		want float64
		tol  float64
	}{
		{"NP", float64(w.NormPower), 140, 0},
		{"AvgPower", float64(w.AvgPower), 123, 0},
		{"AvgPowerActive", float64(w.AvgPowerActive), 145, 2},
		{"CoastingSec", float64(w.CoastingSec), 818, 0},
		{"NoPowerSec", float64(w.NoPowerSec), 100, 3},
		{"WorkKJ", w.WorkKJ, 637, 5},
		{"ElapsedSec", float64(w.ElapsedSec), 5930, 0},
		{"PauseSec", float64(w.PauseSec), 648, 0},
		{"IF", w.IF, 0.897, 0.001},
		{"TSS", w.TSS, 118.2, 1},
		{"VI", w.VI, 1.138, 0.01},
	}
	for _, c := range checks {
		if math.Abs(c.got-c.want) > c.tol {
			t.Errorf("%s = %v, хочу %v ± %v", c.name, c.got, c.want, c.tol)
		}
	}
	checkFinite(t, w.IF, w.TSS, w.VI, w.WorkKJ)
}

// writeSessionFIT — FIT с одной сессией и n записями по 200 Вт с шагом 1 с.
// Поля сессии задаются явно: invalid-маркеры (0xFFFFFFFF) пишутся как есть.
func writeSessionFIT(t *testing.T, path string, totalWork, timerMS, elapsedMS uint32, n int) {
	t.Helper()
	file, err := fit.NewFile(fit.FileTypeActivity, fit.NewHeader(fit.V20, false))
	if err != nil {
		t.Fatal(err)
	}
	act, err := file.Activity()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	sess := fit.NewSessionMsg()
	sess.Sport = fit.SportCycling
	sess.SubSport = fit.SubSportRoad
	sess.StartTime = start
	sess.Timestamp = start.Add(time.Duration(n) * time.Second)
	sess.TotalElapsedTime = elapsedMS
	sess.TotalTimerTime = timerMS
	sess.TotalWork = totalWork
	sess.Event = fit.EventSession
	sess.EventType = fit.EventTypeStop
	act.Sessions = append(act.Sessions, sess)
	for i := 0; i < n; i++ {
		r := fit.NewRecordMsg()
		r.Timestamp = start.Add(time.Duration(i) * time.Second)
		r.Power = 200
		act.Records = append(act.Records, r)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := fit.Encode(f, file, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
}

// TestImportTotalWork — TotalWork из сессии в джоулях: 12000 Дж → 12 кДж.
func TestImportTotalWork(t *testing.T) {
	dir := t.TempDir()
	writeSessionFIT(t, filepath.Join(dir, "work.fit"), 12000, 100000, 100000, 100)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w.WorkKJ-12) > 1e-9 {
		t.Errorf("WorkKJ = %v, хочу 12", w.WorkKJ)
	}
	if w.ElapsedSec != 100 {
		t.Errorf("ElapsedSec = %d, хочу 100", w.ElapsedSec)
	}
}

// TestImportTotalWorkFallback — TotalWork не записан: работа из сэмплов.
func TestImportTotalWorkFallback(t *testing.T) {
	dir := t.TempDir()
	writeSessionFIT(t, filepath.Join(dir, "nowork.fit"), math.MaxUint32, 100000, 100000, 100)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	// 100 записей × 200 Вт × 1 с = 20 кДж
	if math.Abs(w.WorkKJ-20) > 1e-9 {
		t.Errorf("WorkKJ = %v, хочу 20 (fallback по сэмплам)", w.WorkKJ)
	}
}

// TestImportInvalidTimerTime — TotalTimerTime = 0xFFFFFFFF: длительность берётся
// из elapsed, а не превращается в 49.7 дней.
func TestImportInvalidTimerTime(t *testing.T) {
	dir := t.TempDir()
	writeSessionFIT(t, filepath.Join(dir, "timer.fit"), math.MaxUint32, math.MaxUint32, 120000, 100)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if w.Duration != 120*time.Second {
		t.Errorf("Duration = %v, хочу 120 с (из elapsed)", w.Duration)
	}
	// NP и FTP выставляем явно: без них TSS был бы 0 и проверка ничего не ловила
	w.NormPower = 200
	w.FTP = 200
	applyMetrics(w, nil, w.ElapsedSec, w.WorkKJ)
	// duration 120 с, NP=200, FTP=200 → IF=1, TSS ≈ 3.33; взрыв дал бы тысячи
	if !w.HasTSS || w.TSS < 3 || w.TSS > 4 {
		t.Errorf("TSS = %v (has=%v), хочу ≈3.33", w.TSS, w.HasTSS)
	}
}

// TestImportAllTimersInvalid — оба таймера 0xFFFFFFFF: duration 0, TSS не
// считается (NULL), паники и «49.7 дней» нет.
func TestImportAllTimersInvalid(t *testing.T) {
	dir := t.TempDir()
	writeSessionFIT(t, filepath.Join(dir, "notimers.fit"), math.MaxUint32, math.MaxUint32, math.MaxUint32, 100)
	db := openTestDB(t)
	if _, _, err := ImportDir(db, dir); err != nil {
		t.Fatal(err)
	}
	w, err := store.LatestWorkout(db)
	if err != nil {
		t.Fatal(err)
	}
	if w.Duration != 0 {
		t.Errorf("Duration = %v, хочу 0", w.Duration)
	}
	w.NormPower = 200
	w.FTP = 200
	applyMetrics(w, nil, w.ElapsedSec, w.WorkKJ)
	if w.HasTSS || w.TSS != 0 {
		t.Errorf("TSS = %v (has=%v), хочу NULL при duration=0", w.TSS, w.HasTSS)
	}
	if !w.HasIF {
		t.Errorf("IF должен считаться при известных NP/FTP")
	}
}

// TestApplyMetricsAsymmetric — NP≠Avg, FTP≠NP, Duration≠Elapsed: каждая
// формула считает от своего поля и не путает их.
func TestApplyMetricsAsymmetric(t *testing.T) {
	samples := series(90, 1, func(i int) int {
		if i < 30 {
			return 100
		}
		return 300
	})
	w := &store.Workout{
		Duration: 90 * time.Second, ElapsedSec: 120,
		NormPower: 210, AvgPower: 200, FTP: 300,
	}
	applyMetrics(w, samples, 120, 0)

	if math.Abs(w.IF-0.7) > 1e-9 { // 210/300
		t.Errorf("IF = %v, хочу 0.7", w.IF)
	}
	if math.Abs(w.TSS-1.225) > 1e-9 { // 90·210·0.7/(300·3600)·100
		t.Errorf("TSS = %v, хочу 1.225", w.TSS)
	}
	if math.Abs(w.VI-1.05) > 1e-9 { // 210/200
		t.Errorf("VI = %v, хочу 1.05", w.VI)
	}
	if w.AvgPowerActive != 233 { // (30·100 + 60·300) / 90
		t.Errorf("AvgPowerActive = %d, хочу 233", w.AvgPowerActive)
	}
	if w.PauseSec != 30 { // 120 − 90
		t.Errorf("PauseSec = %d, хочу 30", w.PauseSec)
	}
	if math.Abs(w.WorkKJ-21) > 1e-9 { // (30·100 + 60·300) / 1000
		t.Errorf("WorkKJ = %v, хочу 21", w.WorkKJ)
	}
}
