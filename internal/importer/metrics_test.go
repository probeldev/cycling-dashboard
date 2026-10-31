package importer

import (
	"math"
	"os"
	"testing"
	"time"

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
	applyMetrics(w, samples)

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
	applyMetrics(w, samples)

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
	applyMetrics(w, samples)

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
	applyMetrics(w, nil)
	if w.VI != 0 {
		t.Errorf("VI = %v, хочу 0 при avg_power=0", w.VI)
	}
	checkFinite(t, w.IF, w.TSS, w.VI)
}

// TestApplyMetricsPauseClamp — elapsed меньше duration: паузы не отрицательные.
func TestApplyMetricsPauseClamp(t *testing.T) {
	w := &store.Workout{Duration: 100 * time.Second, ElapsedSec: 90}
	applyMetrics(w, nil)
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
	applyMetrics(w, samples)

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
