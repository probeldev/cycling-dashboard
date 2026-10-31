package importer

import (
	"math"
	"testing"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// series — n сэмплов, perSec штук на одну секунду (1 = 1 Гц, 2 = 2 Гц).
// Offset в store.Sample целочисленный, поэтому 2 Гц — это два сэмпла
// на одной секунде, как и получается из настоящих файлов.
func series(n, perSec int, power func(i int) int) []store.Sample {
	out := make([]store.Sample, n)
	for i := range out {
		out[i] = store.Sample{Offset: int64(i / perSec), Power: power(i), HasPwr: true}
	}
	return out
}

// fillForward — копия логики заполнения дропаутов для наивной сверки.
func fillForward(samples []store.Sample) []int {
	pwr := make([]int, len(samples))
	last := 0
	for i, s := range samples {
		if s.HasPwr {
			last = s.Power
		}
		pwr[i] = last
	}
	return pwr
}

// refNP — наивный расчёт NP «в лоб»: для каждого сэмпла среднее по окну
// windowSec секунд. При pauseSec > 0 окно не протягивается назад через разрыв
// больше pauseSec. Нужен, чтобы сверять скользящую реализацию.
func refNP(samples []store.Sample, windowSec, pauseSec int64) float64 {
	pwr := fillForward(samples)
	var sum4 float64
	for i := range samples {
		var roll float64
		var n int
		for j := i; j >= 0; j-- {
			if samples[i].Offset-samples[j].Offset >= windowSec {
				break
			}
			if pauseSec > 0 && j < i && samples[j+1].Offset-samples[j].Offset > pauseSec {
				break
			}
			roll += float64(pwr[j])
			n++
		}
		avg := roll / float64(n)
		avg2 := avg * avg
		sum4 += avg2 * avg2
	}
	return math.Pow(sum4/float64(len(samples)), 0.25)
}

// TestNormalizedPowerWindowIsSeconds — серия 2 Гц: старая реализация брала
// окно в 30 СЭМПЛОВ, то есть 15 секунд. Новая обязана брать 30 секунд.
func TestNormalizedPowerWindowIsSeconds(t *testing.T) {
	// 120 с: первые 60 с по 100 Вт, вторые 60 с по 300 Вт — на ступеньке
	// разница между окном 15 с и 30 с хорошо видна
	samples := series(240, 2, func(i int) int {
		if i < 120 {
			return 100
		}
		return 300
	})

	got := float64(normalizedPower(samples))
	want30 := refNP(samples, npWindowSec, 0)
	want15 := refNP(samples, npWindowSec/2, 0)

	if math.Abs(got-want30) > 1 {
		t.Errorf("NP = %v, хочу окно 30 с (%v)", got, want30)
	}
	if math.Abs(want30-want15) <= 1 {
		t.Fatalf("тест не различает окна: 30 с = %v, 15 с = %v", want30, want15)
	}
	t.Logf("2 Гц, ступенька 100→300 Вт: окно 30 с = %.1f, окно 15 с = %.1f", want30, want15)
}

// TestNormalizedPowerConstant — на постоянной мощности NP равен ей же.
func TestNormalizedPowerConstant(t *testing.T) {
	samples := series(180, 1, func(int) int { return 200 })
	if got := normalizedPower(samples); got != 200 {
		t.Errorf("NP = %d, хочу 200", got)
	}

	// дропауты поверметра заполняются последним значением и результат не портят
	withDropouts := series(180, 1, func(int) int { return 200 })
	for i := 30; i < 60; i++ {
		withDropouts[i] = store.Sample{Offset: int64(i)} // HasPwr = false
	}
	if got := normalizedPower(withDropouts); math.Abs(float64(got)-200) > 1 {
		t.Errorf("NP с дропаутами = %d, хочу 200 ± 1", got)
	}
}

// TestNormalizedPowerPauseBreaksWindow — разрыв на авто-паузе не смешивает
// данные до остановки с данными после неё.
func TestNormalizedPowerPauseBreaksWindow(t *testing.T) {
	// 40 с по 100 Вт, авто-пауза 20 с (меньше окна, поэтому без правила
	// про разрыв старые сэмплы попали бы в окно), затем 40 с по 300 Вт.
	// Стоянку помечаем дистанцией: она не растёт через разрыв.
	var samples []store.Sample
	for i := 0; i < 40; i++ {
		samples = append(samples, store.Sample{
			Offset: int64(i), Power: 100, HasPwr: true, DistanceM: 100, HasDist: true,
		})
	}
	for i := 0; i < 40; i++ {
		samples = append(samples, store.Sample{
			Offset: int64(60 + i), Power: 300, HasPwr: true, DistanceM: 100, HasDist: true,
		})
	}
	if gap := samples[40].Offset - samples[39].Offset; gap <= ridedata.PauseGapSec {
		t.Fatalf("разрыв %d с не считается авто-паузой, тест ничего не проверяет", gap)
	}

	got := float64(normalizedPower(samples))
	wantPaused := refNP(samples, npWindowSec, ridedata.PauseGapSec)
	wantMixed := refNP(samples, npWindowSec, 0)

	if math.Abs(got-wantPaused) > 1 {
		t.Errorf("NP = %v, хочу %v (окно обрывается на паузе)", got, wantPaused)
	}
	if math.Abs(wantPaused-wantMixed) <= 1 {
		t.Errorf("пауза ни на что не влияет: с разрывом %v, без %v", wantPaused, wantMixed)
	}
	t.Logf("с обрывом окна на паузе = %.1f, со смешиванием = %.1f", wantPaused, wantMixed)
}

// TestNormalizedPowerGolden — эталонные значения, посчитанные независимым
// скриптом по спецификации (окно 30 с по ts_offset, x⁴, среднее, ⁴√),
// а не этой же реализацией.
func TestNormalizedPowerGolden(t *testing.T) {
	// пауза помечена дистанцией: она не растёт через разрыв
	var pause []store.Sample
	for i := 0; i < 40; i++ {
		pause = append(pause, store.Sample{
			Offset: int64(i), Power: 100, HasPwr: true, DistanceM: 100, HasDist: true,
		})
	}
	for i := 0; i < 40; i++ {
		pause = append(pause, store.Sample{
			Offset: int64(60 + i), Power: 300, HasPwr: true, DistanceM: 100, HasDist: true,
		})
	}
	saw := series(300, 1, func(i int) int { return 100 + i%50 })
	for i := 0; i < len(saw); i += 17 {
		saw[i].HasPwr = false // дропаут заполняется последним значением
	}

	tests := []struct {
		name    string
		samples []store.Sample
		want    int
	}{
		{"1 Гц, ступенька 100→300 Вт", series(120, 1, func(i int) int {
			if i < 60 {
				return 100
			}
			return 300
		}), 228},
		{"2 Гц, ступенька 100→300 Вт", series(240, 2, func(i int) int {
			if i < 120 {
				return 100
			}
			return 300
		}), 228},
		{"пауза 20 с между 100 и 300 Вт", pause, 253},
		{"пила с дропаутами", saw, 123},
	}
	for _, tt := range tests {
		if got := normalizedPower(tt.samples); math.Abs(float64(got-tt.want)) > 1 {
			t.Errorf("%s: NP = %d, хочу %d ± 1", tt.name, got, tt.want)
		}
	}
}

// TestNormalizedPowerDeterministic — повторный вызов даёт тот же результат
// и не портит исходные сэмплы.
func TestNormalizedPowerDeterministic(t *testing.T) {
	samples := series(200, 1, func(i int) int { return 50 + i%300 })
	for i := 60; i < 70; i++ {
		samples[i] = store.Sample{Offset: int64(i)} // дропаут
	}
	before := make([]store.Sample, len(samples))
	copy(before, samples)

	first := normalizedPower(samples)
	second := normalizedPower(samples)
	if first != second {
		t.Errorf("недетерминированно: %d и %d", first, second)
	}
	for i := range samples {
		if samples[i] != before[i] {
			t.Fatalf("сэмпл %d изменён: %+v → %+v", i, before[i], samples[i])
		}
	}

	if got := normalizedPower(nil); got != 0 {
		t.Errorf("NP пустой серии = %d, хочу 0", got)
	}
	if got := normalizedPower(series(1, 1, func(int) int { return 150 })); got != 150 {
		t.Errorf("NP из одного сэмпла = %d, хочу 150", got)
	}
}

// TestNormalizedPowerUniformSampleRate — равномерная запись с шагом 3 с даёт
// тот же NP, что эквивалентная 1 Гц: окна взвешиваются по длительности.
func TestNormalizedPowerUniformSampleRate(t *testing.T) {
	oneHz := series(120, 1, func(i int) int {
		if i < 60 {
			return 100
		}
		return 300
	})
	threeSec := series(40, 1, func(i int) int {
		if i < 20 {
			return 100
		}
		return 300
	})
	for i := range threeSec {
		threeSec[i].Offset *= 3
	}
	if a, b := normalizedPower(oneHz), normalizedPower(threeSec); a != b {
		t.Errorf("1 Гц NP=%d, шаг 3 с NP=%d; хочу одинаковый", a, b)
	}
}

// TestNormalizedPowerMixedSampleRate — плотный и редкий участки равной
// длительности вносят равный вклад. Эталон 219 посчитан независимым скриптом;
// без взвешивания получилось бы 188.
func TestNormalizedPowerMixedSampleRate(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 60; i++ { // 60 с по 100 Вт с шагом 1 с
		samples = append(samples, store.Sample{Offset: int64(i), Power: 100, HasPwr: true})
	}
	for i := 0; i < 20; i++ { // 60 с по 300 Вт с шагом 3 с
		samples = append(samples, store.Sample{Offset: int64(60 + 3*i), Power: 300, HasPwr: true})
	}
	if got := normalizedPower(samples); math.Abs(float64(got)-219) > 1 {
		t.Errorf("NP = %d, хочу 219 (взвешивание по длительности)", got)
	}
}
