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
	return sum(secsSlice(entries))
}

func secsSlice(entries []distEntry) []int {
	out := make([]int, len(entries))
	for i, e := range entries {
		out[i] = e.Secs
	}
	return out
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
