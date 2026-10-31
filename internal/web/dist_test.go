package web

import (
	"encoding/json"
	"math"
	"testing"

	"cycling-dashboard/internal/store"
)

// pwr — сэмпл с мощностью; has=false означает дропаут поверметра (данных нет),
// а не честный ноль.
func pwr(power int, has bool) store.Sample {
	return store.Sample{Power: power, HasPwr: has}
}

// rep — n одинаковых сэмплов.
func rep(n int, s store.Sample) []store.Sample {
	out := make([]store.Sample, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// seq — склеивает группы сэмплов и проставляет offsets подряд с шагом 1 с,
// чтобы каждый сэмпл давал в распределение ровно одну секунду.
func seq(groups ...[]store.Sample) []store.Sample {
	var out []store.Sample
	for _, g := range groups {
		out = append(out, g...)
	}
	for i := range out {
		out[i].Offset = int64(i)
	}
	return out
}

func secsOf(entries []distEntry, label string) int {
	for _, e := range entries {
		if e.Label == label {
			return e.Secs
		}
	}
	return -1
}

func totalSecs(entries []distEntry) int {
	total := 0
	for _, e := range entries {
		total += e.Secs
	}
	return total
}

// mixed — 10 с наката, 20 на 120 Вт, 5 дропаутов, 10 на 250 Вт.
func mixed() []store.Sample {
	return seq(
		rep(10, pwr(0, true)),
		rep(20, pwr(120, true)),
		rep(5, pwr(0, false)),
		rep(10, pwr(250, true)),
	)
}

func TestDistributionsCoastBucket(t *testing.T) {
	d := distributions(mixed(), 0)
	power, ok := d.Buckets["power"]
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if power[0].Label != coastLabel {
		t.Errorf("первый бакет = %q, хочу %q", power[0].Label, coastLabel)
	}
	if got := power[0].Secs; got != 10 {
		t.Errorf("накат = %d с, хочу 10", got)
	}
	if got := secsOf(power, "100–150"); got != 20 {
		t.Errorf("100–150 Вт = %d с, хочу 20", got)
	}
	if got := secsOf(power, "250–300"); got != 10 {
		t.Errorf("250–300 Вт = %d с, хочу 10", got)
	}
}

func TestDistributionsDropoutsNotCounted(t *testing.T) {
	d := distributions(mixed(), 0)
	power := d.Buckets["power"]
	// 45 сэмплов, из них 5 — дропауты: учтены только 40
	if got := totalSecs(power); got != 40 {
		t.Errorf("учтено %d с, хочу 40 (дропауты должны выпадать)", got)
	}
	if got := d.PowerBase; got.CoastSecs != 10 || got.PedalSecs != 30 || got.TotalSecs != 40 {
		t.Errorf("базы = %+v, хочу coast=10 pedal=30 total=40", got)
	}

	// все сэмплы — дропауты: распределения мощности нет вовсе
	all := distributions(rep(5, pwr(0, false)), 0)
	if _, ok := all.Buckets["power"]; ok {
		t.Error("дропауты дали распределение мощности, хочу его отсутствие")
	}
	if _, ok := all.Buckets["cadence"]; ok {
		t.Error("пустой сэмпл дал распределение каденса")
	}
}

func TestDistributionsCoastWithZones(t *testing.T) {
	// при FTP=200 зона Z1 начинается с нуля — накат не должен в неё провалиться
	d := distributions(mixed(), 200)
	power := d.Buckets["power"]
	if power[0].Label != coastLabel {
		t.Errorf("первый бакет = %q, хочу %q", power[0].Label, coastLabel)
	}
	if got := secsOf(power, "Z1 · 0–111"); got != 0 {
		t.Errorf("в Z1 попало %d с наката, хочу 0", got)
	}
	if got := secsOf(power, "Z2 · 112–151"); got != 20 {
		t.Errorf("Z2 = %d с, хочу 20 (120 Вт)", got)
	}
	if got := secsOf(power, "Z6 · 242–301"); got != 10 {
		t.Errorf("Z6 = %d с, хочу 10 (250 Вт)", got)
	}
}

// TestSampleSecondsPedalingWithoutSpeed — ни дистанции, ни скорости нет, но
// мощность и каденс есть: это прореживание записи при движении, а не пауза.
func TestSampleSecondsPedalingWithoutSpeed(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, Cadence: 90, HasCad: true},
		{Offset: 30, Power: 120, HasPwr: true, Cadence: 90, HasCad: true},
	}
	times := sampleSeconds(samples)
	if times[0] != 30 {
		t.Errorf("педалирование без скорости: %.1f с, хочу 30", times[0])
	}
	if times[1] != 1 {
		t.Errorf("последняя запись: %.1f с, хочу 1", times[1])
	}
}

// TestSampleSecondsStopWithDistance — тот же разрыв, но дистанция не выросла:
// это стоянка, даже если в записи осталась мощность.
func TestSampleSecondsStopWithDistance(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, DistanceM: 100, HasDist: true},
		{Offset: 30, Power: 120, HasPwr: true, DistanceM: 100, HasDist: true},
	}
	if got := sampleSeconds(samples)[0]; got != 1 {
		t.Errorf("стоянка с мощностью: %.1f с, хочу 1", got)
	}
}

// TestSampleSecondsSmartRecording — редкая запись при движении (smart recording)
// не пауза: интервал засчитывается целиком.
func TestSampleSecondsSmartRecording(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
		{Offset: 30, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
	}
	if got := sampleSeconds(samples)[0]; got != 30 {
		t.Errorf("движение на 20 км/ч: %.1f с, хочу 30", got)
	}
}

// TestSampleSecondsCoastSlowRecording — накат на скорости при шаге записи 10 с:
// дистанция растёт, значит ехали, и все 10 с — накат.
func TestSampleSecondsCoastSlowRecording(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 0, HasDist: true},
		{Offset: 10, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 41.7, HasDist: true},
	}
	if got := sampleSeconds(samples)[0]; got != 10 {
		t.Errorf("качение на 15 км/ч: %.1f с, хочу 10", got)
	}
}

// TestSampleSecondsCoastStop — накат, потом стоянка: 10 с интервал не накат.
func TestSampleSecondsCoastStop(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true},
		{Offset: 10, Power: 0, HasPwr: true},
	}
	if got := sampleSeconds(samples)[0]; got != 1 {
		t.Errorf("стоянка на 0 Вт: %.1f с, хочу 1", got)
	}
}

// TestSampleSecondsStaleSpeed — как на тренировке 418: перед авто-паузой в записи
// осталась последняя «ещё ехавшая» скорость 7.5 км/ч, но дистанция не выросла.
func TestSampleSecondsStaleSpeed(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 7.5 / 3.6, DistanceM: 361.74, HasDist: true},
		{Offset: 37, Power: 0, HasPwr: true, DistanceM: 361.74, HasDist: true},
	}
	if got := sampleSeconds(samples)[0]; got != 1 {
		t.Errorf("стоянка со «старой» скоростью: %.1f с, хочу 1 (дистанция не выросла)", got)
	}
}

// TestSampleSecondsMovingGapCap — ехали, но запись пропала надолго: потолок maxGapSec.
func TestSampleSecondsMovingGapCap(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
		{Offset: 120, Power: 200, HasPwr: true, SpeedMs: 20 / 3.6},
	}
	if got := sampleSeconds(samples)[0]; got != maxGapSec {
		t.Errorf("движение с разрывом 120 с: %.1f с, хочу потолок %d", got, maxGapSec)
	}
}

// TestSampleSecondsDuplicateOffsets — запись 2 Гц: две записи на секунду делят
// интервал поровну, и нулевая мощность не растворяется в усреднении.
func TestSampleSecondsDuplicateOffsets(t *testing.T) {
	var samples []store.Sample
	for sec := 0; sec < 10; sec++ {
		samples = append(samples,
			store.Sample{Offset: int64(sec), Power: 0, HasPwr: true},
			store.Sample{Offset: int64(sec), Power: 200, HasPwr: true})
	}
	for i, got := range sampleSeconds(samples) {
		if got != 0.5 {
			t.Fatalf("запись %d: %.1f с, хочу 0.5", i, got)
		}
	}

	d := distributions(samples, 0)
	if got := d.PowerBase.CoastSecs; got != 5 {
		t.Errorf("накат = %d с, хочу 5 (10 записей × 0.5 с)", got)
	}
	if got := d.PowerBase.PedalSecs; got != 5 {
		t.Errorf("педалирование = %d с, хочу 5", got)
	}
	if got := d.PowerBase.TotalSecs; got != 10 {
		t.Errorf("total = %d с, хочу 10", got)
	}
}

// TestSampleSecondsSparseOffsets — прореженная запись (0, 3, 10, 11) при движении:
// каждая запись получает интервал до следующей, накат не теряется.
func TestSampleSecondsSparseOffsets(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 0, HasDist: true},
		{Offset: 3, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 12.5, HasDist: true},
		{Offset: 10, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 41.7, HasDist: true},
		{Offset: 11, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 45.8, HasDist: true},
	}
	d := distributions(samples, 0)
	if got := d.PowerBase.CoastSecs; got != 3 {
		t.Errorf("накат = %d с, хочу 3 (интервал 0→3)", got)
	}
	// 3→10 — 7 с педалирования, 10→11 — 1 с, последняя запись — 1 с
	if got := d.PowerBase.PedalSecs; got != 9 {
		t.Errorf("педалирование = %d с, хочу 9", got)
	}
}

// TestDistributionsEdgeCases — пустой вход, одна запись, весь накат, все дропауты.
func TestDistributionsEdgeCases(t *testing.T) {
	empty := distributions(nil, 0)
	if len(empty.Buckets) != 0 || empty.PowerBase.TotalSecs != 0 {
		t.Errorf("пустой вход: бакеты %+v, база %+v", empty.Buckets, empty.PowerBase)
	}

	single := distributions([]store.Sample{{Power: 200, HasPwr: true}}, 0)
	if got := single.PowerBase.TotalSecs; got != 1 {
		t.Errorf("одна запись: total = %d, хочу 1", got)
	}

	coast := distributions(seq(rep(5, pwr(0, true))), 0)
	if got := coast.PowerBase; got.CoastSecs != 5 || got.PedalSecs != 0 || got.TotalSecs != 5 {
		t.Errorf("весь накат: %+v, хочу coast=5 pedal=0 total=5", got)
	}

	dropouts := distributions(seq(rep(5, pwr(0, false))), 0)
	if len(dropouts.Buckets) != 0 {
		t.Errorf("все дропауты дали бакеты: %+v", dropouts.Buckets)
	}

	// проценты должны быть в JSON у всех бакетов, иначе клиент получит undefined
	withCad := distributions(seq(rep(3, store.Sample{Cadence: 85, HasCad: true})), 0)
	raw, err := json.Marshal(withCad)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string][]map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"pct", "pctTotal"} {
		if _, ok := top["cadence"][0][key]; !ok {
			t.Errorf("у cadence нет поля %q — клиент получит undefined", key)
		}
	}
}

// TestDistributionsPauseAppliesToZones — правило паузы общее для всех power-бакетов:
// 30 с стоянки (дистанция не выросла) не попадают в зону, где была мощность
// перед остановкой.
func TestDistributionsPauseAppliesToZones(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, DistanceM: 500, HasDist: true},
		{Offset: 30, Power: 120, HasPwr: true, DistanceM: 500, HasDist: true},
	}
	d := distributions(samples, 200)
	if got := secsOf(d.Buckets["power"], "Z2 · 112–151"); got != 2 {
		t.Errorf("Z2 = %d с, хочу 2 (по секунде за запись, без 30 с стоянки)", got)
	}
	if got := d.PowerBase.TotalSecs; got != 2 {
		t.Errorf("totalSecs = %d, хочу 2", got)
	}
}

// TestDistributionsCoastExcludesPauses — разрыв на авто-паузе после наката
// означает остановку: эти секунды не должны попадать в бакет наката.
func TestDistributionsCoastExcludesPauses(t *testing.T) {
	coast := func(offset int64) store.Sample {
		return store.Sample{Offset: offset, Power: 0, HasPwr: true}
	}
	pedal := func(offset int64) store.Sample {
		return store.Sample{Offset: offset, Power: 100, HasPwr: true}
	}

	// короткий разрыв (3 с) — ещё качение, накат продолжается
	short := distributions([]store.Sample{coast(0), coast(1), pedal(4), pedal(5)}, 0)
	if got := short.Buckets["power"][0].Secs; got != 4 {
		t.Errorf("накат с коротким разрывом = %d с, хочу 4", got)
	}

	// разрыв 30 с — авто-пауза: засчитываем только секунды самих сэмплов
	long := distributions([]store.Sample{coast(0), coast(1), pedal(31), pedal(32)}, 0)
	power := long.Buckets["power"]
	if got := power[0].Secs; got != 2 {
		t.Errorf("накат перед авто-паузой = %d с, хочу 2", got)
	}
	if got := long.PowerBase; got.CoastSecs != 2 || got.PedalSecs != 2 || got.TotalSecs != 4 {
		t.Errorf("базы = %+v, хочу coast=2 pedal=2 total=4", got)
	}
}

func TestDistributionsPercentsConsistent(t *testing.T) {
	d := distributions(mixed(), 0)
	power := d.Buckets["power"]

	var pct, pctTotal float64
	for i, e := range power {
		pct += e.Pct
		pctTotal += e.PctTotal
		if i == 0 && e.Pct != 0 {
			t.Errorf("у бакета наката pct = %v, хочу 0 (накат не педалирование)", e.Pct)
		}
	}
	// округление до 0.1 на восьми бакетах даёт погрешность максимум в пару десятых
	if math.Abs(pct-100) > 0.5 {
		t.Errorf("сумма pct = %v, хочу 100", pct)
	}
	if math.Abs(pctTotal-100) > 0.5 {
		t.Errorf("сумма pctTotal = %v, хочу 100", pctTotal)
	}
	// накат: 10 с из 40 общих, но 0 из 30 педалирования
	if got := power[0].PctTotal; got != 25 {
		t.Errorf("pctTotal наката = %v, хочу 25", got)
	}
	if got := secsPct(power, "100–150"); got != 66.7 {
		t.Errorf("pct для 100–150 Вт = %v, хочу 66.7 (20 из 30 с педалирования)", got)
	}
}

func secsPct(entries []distEntry, label string) float64 {
	for _, e := range entries {
		if e.Label == label {
			return e.Pct
		}
	}
	return -1
}

func TestDistributionsOtherMetricsUnchanged(t *testing.T) {
	// нули по каденсу/скорости/пульсу по-прежнему не учитываются
	samples := seq(
		rep(10, store.Sample{}),
		rep(10, store.Sample{Cadence: 85, HasCad: true, HR: 140, SpeedMs: 8}),
	)
	d := distributions(samples, 0)

	for kind, want := range map[string]struct {
		label string
		secs  int
	}{
		"cadence": {"80–90", 10},
		"speed":   {"25–30", 10},
		"hr":      {"140–150", 10},
	} {
		entries, ok := d.Buckets[kind]
		if !ok {
			t.Fatalf("нет распределения %s", kind)
		}
		if got := secsOf(entries, want.label); got != want.secs {
			t.Errorf("%s: бакет %s = %d с, хочу %d", kind, want.label, got, want.secs)
		}
		if got := totalSecs(entries); got != want.secs {
			t.Errorf("%s: учтено %d с, хочу %d (нули должны выпадать)", kind, got, want.secs)
		}
		if entries[0].Label == coastLabel {
			t.Errorf("%s: появился бакет наката, его быть не должно", kind)
		}
		for _, e := range entries {
			if e.Pct != 0 || e.PctTotal != 0 {
				t.Errorf("%s: проценты %v/%v, хочу 0 (считаются только для мощности)",
					kind, e.Pct, e.PctTotal)
			}
		}
	}
	if _, ok := d.Buckets["power"]; ok {
		t.Error("без данных мощности появилось распределение мощности")
	}
}

// TestDistJSONContract — поля можно добавлять, удалять и переименовывать нельзя.
func TestDistJSONContract(t *testing.T) {
	raw, err := json.Marshal(distributions(mixed(), 200))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(top["power"], &entries); err != nil {
		t.Fatalf("power должен остаться массивом бакетов: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("пустой массив бакетов мощности")
	}
	for _, key := range []string{"label", "secs", "pctTotal"} {
		if _, ok := entries[0][key]; !ok {
			t.Errorf("в бакете мощности нет поля %q", key)
		}
	}
	var base map[string]int
	if err := json.Unmarshal(top["powerBase"], &base); err != nil {
		t.Fatalf("powerBase должен быть объектом: %v", err)
	}
	for _, key := range []string{"coastSecs", "pedalSecs", "totalSecs"} {
		if _, ok := base[key]; !ok {
			t.Errorf("в powerBase нет поля %q", key)
		}
	}
	if base["coastSecs"] != 10 || base["pedalSecs"] != 30 || base["totalSecs"] != 40 {
		t.Errorf("powerBase = %v, хочу coast=10 pedal=30 total=40", base)
	}
}
