package hrtss

import (
	"math"
	"testing"
)

// training возвращает обучающую тренировку с заданным HR и TSS/час.
func training(hr int, tssPerHour float64, hours float64) Ride {
	return Ride{AvgHR: hr, DurationSec: int64(hours * 3600), TSS: tssPerHour * hours}
}

func TestFitExactLinear(t *testing.T) {
	// y = 10 + 1.5*hr; TSS/час линейно от пульса
	rides := []Ride{
		training(120, 10+1.5*120, 1.0),
		training(130, 10+1.5*130, 1.5),
		training(140, 10+1.5*140, 2.0),
		training(150, 10+1.5*150, 1.0),
		training(160, 10+1.5*160, 2.5),
	}
	m, ok := Fit(rides)
	if !ok {
		t.Fatal("Fit должен быть ok")
	}
	if math.Abs(m.a-10) > 1e-9 || math.Abs(m.b-1.5) > 1e-9 {
		t.Errorf("a=%v b=%v, хочу 10 и 1.5", m.a, m.b)
	}
	if m.minHR != 120 || m.maxHR != 160 {
		t.Errorf("min/max HR = %d/%d, хочу 120/160", m.minHR, m.maxHR)
	}

	tss, low, ok := m.Estimate(145, 3600)
	if !ok {
		t.Fatal("Estimate должен быть ok")
	}
	want := (10 + 1.5*145)
	if math.Abs(tss-want) > 1e-6 {
		t.Errorf("TSS = %v, хочу %v", tss, want)
	}
	if low {
		t.Error("HR внутри диапазона: lowConfidence должен быть false")
	}
}

func TestFitShorterDuration(t *testing.T) {
	rides := []Ride{
		training(130, 130, 1.0),
		training(140, 140, 1.5),
		training(150, 150, 2.0),
		training(160, 160, 1.0),
		training(170, 170, 2.5),
	}
	m, ok := Fit(rides)
	if !ok {
		t.Fatal("Fit должен быть ok")
	}
	// 30 минут при 130 уд/мин и TSS/час = 130 → 65 TSS
	tss, _, ok := m.Estimate(130, 1800)
	if !ok {
		t.Fatal("Estimate должен быть ok")
	}
	if math.Abs(tss-65) > 1e-6 {
		t.Errorf("TSS = %v, хочу 65", tss)
	}
}

func TestFitTooFewRides(t *testing.T) {
	rides := []Ride{training(130, 100, 1), training(160, 100, 1)}
	if _, ok := Fit(rides); ok {
		t.Error("<MinRides (3) тренировок: Fit должен вернуть ok=false")
	}
}

func TestFitMinRides(t *testing.T) {
	// ровно MinRides=3 тренировки с достаточным разбросом пульса — модель строится
	rides := []Ride{
		training(130, 10+1.5*130, 1.0),
		training(145, 10+1.5*145, 1.5),
		training(160, 10+1.5*160, 2.0),
	}
	m, ok := Fit(rides)
	if !ok {
		t.Fatal("3 тренировки: Fit должен быть ok")
	}
	if m.n != 3 {
		t.Errorf("n = %d, хочу 3", m.n)
	}
	// но все оценки при n < 5 — lowConfidence, даже внутри диапазона
	if _, low, ok := m.Estimate(145, 3600); !ok || !low {
		t.Errorf("n=3, HR внутри диапазона: ok=%v low=%v, хочу ok=true low=true", ok, low)
	}
}

func TestFitNarrowHRSpread(t *testing.T) {
	// разброс пульса 150-140 = 10 < 15: регрессия вырождена
	rides := []Ride{
		training(140, 100, 1),
		training(145, 110, 1),
		training(150, 120, 1),
		training(148, 115, 1),
		training(142, 105, 1),
	}
	if _, ok := Fit(rides); ok {
		t.Error("разброс HR < 15: Fit должен вернуть ok=false")
	}
}

func TestEstimateLowConfidenceFewRides(t *testing.T) {
	// 4 тренировки: модель строится, но оценки — lowConfidence
	rides := []Ride{
		training(130, 100, 1),
		training(140, 110, 1),
		training(150, 120, 1),
		training(160, 130, 1),
	}
	m, ok := Fit(rides)
	if !ok {
		t.Fatal("Fit должен быть ok")
	}
	if _, low, ok := m.Estimate(145, 3600); !ok || !low {
		t.Errorf("n=4, HR внутри диапазона: ok=%v low=%v, хочу ok=true low=true", ok, low)
	}
}

func TestFitZeroDuration(t *testing.T) {
	rides := []Ride{
		training(130, 100, 1),
		training(140, 100, 1),
		training(150, 100, 1),
		training(160, 100, 1),
		{AvgHR: 170, DurationSec: 0, TSS: 0},
	}
	if _, ok := Fit(rides); ok {
		t.Error("нулевая длительность: Fit должен вернуть ok=false")
	}
}

func TestFitZeroHR(t *testing.T) {
	rides := []Ride{
		training(130, 100, 1),
		training(140, 100, 1),
		training(150, 100, 1),
		training(160, 100, 1),
		{AvgHR: 0, DurationSec: 3600, TSS: 100},
	}
	if _, ok := Fit(rides); ok {
		t.Error("нулевой пульс: Fit должен вернуть ok=false")
	}
}

func TestFitSameHR(t *testing.T) {
	rides := []Ride{
		training(140, 80, 1),
		training(140, 90, 1),
		training(140, 100, 1),
		training(140, 110, 1),
		training(140, 120, 1),
	}
	if _, ok := Fit(rides); ok {
		t.Error("одинаковый HR: Fit должен вернуть ok=false")
	}
}

func TestFitNegativeSlope(t *testing.T) {
	rides := []Ride{
		training(130, 200, 1),
		training(140, 190, 1),
		training(150, 180, 1),
		training(160, 170, 1),
		training(170, 160, 1),
	}
	if _, ok := Fit(rides); ok {
		t.Error("b <= 0: Fit должен вернуть ok=false")
	}
}

func TestEstimateLowConfidence(t *testing.T) {
	rides := []Ride{
		training(130, 100, 1),
		training(140, 110, 1),
		training(150, 120, 1),
		training(160, 130, 1),
		training(170, 140, 1),
	}
	m, ok := Fit(rides)
	if !ok {
		t.Fatal("Fit должен быть ok")
	}
	for _, hr := range []int{130, 170} {
		_, low, ok := m.Estimate(hr, 3600)
		if !ok || low {
			t.Errorf("HR=%d: ok=%v low=%v, хочу ok=true low=false", hr, ok, low)
		}
	}
	for _, hr := range []int{124, 176} {
		_, low, ok := m.Estimate(hr, 3600)
		if !ok || !low {
			t.Errorf("HR=%d за пределами ±5: ok=%v low=%v, хочу ok=true low=true", hr, ok, low)
		}
	}
}

func TestEstimateInvalid(t *testing.T) {
	rides := []Ride{
		training(130, 100, 1),
		training(140, 110, 1),
		training(150, 120, 1),
		training(160, 130, 1),
		training(170, 140, 1),
	}
	m, _ := Fit(rides)
	if _, _, ok := m.Estimate(0, 3600); ok {
		t.Error("HR=0: Estimate ok=false")
	}
	if _, _, ok := m.Estimate(150, 0); ok {
		t.Error("duration=0: Estimate ok=false")
	}
	// модель с отрицательным свободным членом даст отрицательный TSS при низком HR
	bad := Model{a: -1000, b: 1, minHR: 130, maxHR: 170}
	if _, _, ok := bad.Estimate(131, 3600); ok {
		t.Error("оценка <=0: ok=false")
	}
	// слишком большой TSS
	huge := Model{a: 0, b: 1000, minHR: 130, maxHR: 170}
	if _, _, ok := huge.Estimate(200, 3600); ok {
		t.Error("оценка >500: ok=false")
	}
}
