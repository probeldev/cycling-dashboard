package web

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/ridedata"
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

func totalSecs(entries []distEntry, from int) int {
	total := 0
	for _, e := range entries[from:] {
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

func TestZoneDistributionCoastBucket(t *testing.T) {
	entries, ok := zoneDistribution(mixed(), 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if entries[0].Label != coastLabel {
		t.Errorf("первый бакет = %q, хочу %q", entries[0].Label, coastLabel)
	}
	if got := entries[0].Secs; got != 10 {
		t.Errorf("накат = %d с, хочу 10", got)
	}
	if got := secsOf(entries, "Z2 · 112–152"); got != 20 {
		t.Errorf("Z2 = %d с, хочу 20 (120 Вт)", got)
	}
	if got := secsOf(entries, "Z6 · 242–302"); got != 10 {
		t.Errorf("Z6 = %d с, хочу 10 (250 Вт)", got)
	}
}

func TestZoneDistributionDropoutsNotCounted(t *testing.T) {
	entries, ok := zoneDistribution(mixed(), 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	// 45 сэмплов, из них 5 — дропауты: учтены только 40
	if got := totalSecs(entries, 0); got != 40 {
		t.Errorf("учтено %d с, хочу 40 (дропауты должны выпадать)", got)
	}

	// все сэмплы — дропауты: распределения мощности нет вовсе
	if _, ok := zoneDistribution(rep(5, pwr(0, false)), 200); ok {
		t.Error("дропауты дали распределение мощности, хочу его отсутствие")
	}
	if _, ok := metricDistributions(rep(5, pwr(0, false)))["cadence"]; ok {
		t.Error("пустой сэмпл дал распределение каденса")
	}
}

func TestZoneDistributionCoastWithZones(t *testing.T) {
	// при FTP=200 зона Z1 начинается с нуля — накат не должен в неё провалиться
	entries, ok := zoneDistribution(mixed(), 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := secsOf(entries, "Z1 · 0–112"); got != 0 {
		t.Errorf("в Z1 попало %d с наката, хочу 0", got)
	}
	if got := secsOf(entries, "Z2 · 112–152"); got != 20 {
		t.Errorf("Z2 = %d с, хочу 20 (120 Вт)", got)
	}
	if got := secsOf(entries, "Z6 · 242–302"); got != 10 {
		t.Errorf("Z6 = %d с, хочу 10 (250 Вт)", got)
	}
}

// TestZoneDistributionDuplicateOffsets — запись 2 Гц: две записи на секунду делят
// время поровну, и нулевая мощность не растворяется в усреднении.
func TestZoneDistributionDuplicateOffsets(t *testing.T) {
	var samples []store.Sample
	for sec := 0; sec < 10; sec++ {
		samples = append(samples,
			store.Sample{Offset: int64(sec), Power: 0, HasPwr: true},
			store.Sample{Offset: int64(sec), Power: 200, HasPwr: true})
	}
	entries, ok := zoneDistribution(samples, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := entries[0].Secs; got != 5 {
		t.Errorf("накат = %d с, хочу 5", got)
	}
	if got := totalSecs(entries, 0); got != 10 {
		t.Errorf("total = %d с, хочу 10", got)
	}
}

// TestZoneDistributionSparseOffsets — прореженная запись (0, 3, 10, 11) при
// движении: каждая запись получает интервал до следующей, накат не теряется.
func TestZoneDistributionSparseOffsets(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 0, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 0, HasDist: true},
		{Offset: 3, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 12.5, HasDist: true},
		{Offset: 10, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 41.7, HasDist: true},
		{Offset: 11, Power: 200, HasPwr: true, SpeedMs: 15 / 3.6, DistanceM: 45.8, HasDist: true},
	}
	entries, ok := zoneDistribution(samples, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := entries[0].Secs; got != 3 {
		t.Errorf("накат = %d с, хочу 3 (интервал 0→3)", got)
	}
	// 3→10 — 7 с педалирования, 10→11 — 1 с, последняя запись — 1 с
	if got := totalSecs(entries, 1); got != 9 {
		t.Errorf("педалирование = %d с, хочу 9", got)
	}
}

// TestZoneDistributionEdgeCases — пустой вход, одна запись, весь накат,
// все дропауты.
func TestZoneDistributionEdgeCases(t *testing.T) {
	if _, ok := zoneDistribution(nil, 200); ok {
		t.Error("пустой вход дал распределение мощности")
	}

	single, ok := zoneDistribution([]store.Sample{{Power: 200, HasPwr: true}}, 200)
	if !ok {
		t.Fatal("одна запись не дала распределения мощности")
	}
	if got := totalSecs(single, 0); got != 1 {
		t.Errorf("одна запись: total = %d, хочу 1", got)
	}

	coast, ok := zoneDistribution(seq(rep(5, pwr(0, true))), 200)
	if !ok {
		t.Fatal("весь накат не дал распределения мощности")
	}
	if got := totalSecs(coast, 0); got != 5 {
		t.Errorf("весь накат: total = %d, хочу 5", got)
	}
	if got := totalSecs(coast, 1); got != 0 {
		t.Errorf("весь накат: педалирование = %d, хочу 0", got)
	}

	if _, ok := zoneDistribution(seq(rep(5, pwr(0, false))), 200); ok {
		t.Error("все дропауты дали распределение мощности")
	}

	// пустые диапазоны остаются в срезе, чтобы индексы зон не съезжали
	withCad := metricDistributions(seq(rep(3, store.Sample{Cadence: 85, HasCad: true})))
	entries, ok := withCad["cadence"]
	if !ok {
		t.Fatal("нет распределения каденса")
	}
	if len(entries) != len(distDefs["cadence"]) {
		t.Errorf("бакетов каденса %d, хочу %d", len(entries), len(distDefs["cadence"]))
	}
}

// TestZoneDistributionPauseAppliesToZones — правило паузы общее для всех
// power-бакетов: 30 с стоянки (дистанция не выросла) не попадают в зону, где
// была мощность перед остановкой.
func TestZoneDistributionPauseAppliesToZones(t *testing.T) {
	samples := []store.Sample{
		{Offset: 0, Power: 120, HasPwr: true, DistanceM: 500, HasDist: true},
		{Offset: 30, Power: 120, HasPwr: true, DistanceM: 500, HasDist: true},
	}
	entries, ok := zoneDistribution(samples, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := secsOf(entries, "Z2 · 112–152"); got != 2 {
		t.Errorf("Z2 = %d с, хочу 2 (по секунде за запись, без 30 с стоянки)", got)
	}
	if got := totalSecs(entries, 0); got != 2 {
		t.Errorf("total = %d, хочу 2", got)
	}
}

// TestZoneDistributionCoastExcludesPauses — разрыв на авто-паузе после наката
// означает остановку: эти секунды не должны попадать в бакет наката.
func TestZoneDistributionCoastExcludesPauses(t *testing.T) {
	coast := func(offset int64) store.Sample {
		return store.Sample{Offset: offset, Power: 0, HasPwr: true}
	}
	pedal := func(offset int64) store.Sample {
		return store.Sample{Offset: offset, Power: 100, HasPwr: true}
	}

	// короткий разрыв (3 с) — ещё качение, накат продолжается
	short, ok := zoneDistribution([]store.Sample{coast(0), coast(1), pedal(4), pedal(5)}, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := short[0].Secs; got != 4 {
		t.Errorf("накат с коротким разрывом = %d с, хочу 4", got)
	}

	// разрыв 30 с — авто-пауза: засчитываем только секунды самих сэмплов
	long, ok := zoneDistribution([]store.Sample{coast(0), coast(1), pedal(31), pedal(32)}, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if got := long[0].Secs; got != 2 {
		t.Errorf("накат перед авто-паузой = %d с, хочу 2", got)
	}
	if got := totalSecs(long, 0); got != 4 {
		t.Errorf("total = %d, хочу 4", got)
	}
}

func TestZoneDistributionPercentsConsistent(t *testing.T) {
	entries, ok := zoneDistribution(mixed(), 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}

	var pct float64
	for i, e := range entries {
		pct += e.Pct
		if i == 0 && e.Pct != 0 {
			t.Errorf("у бакета наката pct = %v, хочу 0 (накат не педалирование)", e.Pct)
		}
	}
	// округление до 0.1 на восьми бакетах даёт погрешность максимум в пару десятых
	if math.Abs(pct-100) > 0.5 {
		t.Errorf("сумма pct = %v, хочу 100", pct)
	}
	if got := secsPct(entries, "Z2 · 112–152"); got != 66.7 {
		t.Errorf("pct для Z2 = %v, хочу 66.7 (20 из 30 с педалирования)", got)
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

func TestMetricDistributionsExcludeZeros(t *testing.T) {
	// нули по каденсу/скорости/пульсу по-прежнему не учитываются
	samples := seq(
		rep(10, store.Sample{}),
		rep(10, store.Sample{Cadence: 85, HasCad: true, HR: 140, SpeedMs: 8}),
	)
	d := metricDistributions(samples)

	for kind, want := range map[string]struct {
		label string
		secs  int
	}{
		"cadence": {"80–90", 10},
		"speed":   {"25–30", 10},
		"hr":      {"140–150", 10},
	} {
		entries, ok := d[kind]
		if !ok {
			t.Fatalf("нет распределения %s", kind)
		}
		if got := secsOf(entries, want.label); got != want.secs {
			t.Errorf("%s: бакет %s = %d с, хочу %d", kind, want.label, got, want.secs)
		}
		if got := totalSecs(entries, 0); got != want.secs {
			t.Errorf("%s: учтено %d с, хочу %d (нули должны выпадать)", kind, got, want.secs)
		}
		if entries[0].Label == coastLabel {
			t.Errorf("%s: появился бакет наката, его быть не должно", kind)
		}
		if got := secsPct(entries, want.label); got != 100 {
			t.Errorf("%s: pct бакета %s = %v, хочу 100", kind, want.label, got)
		}
	}
	if _, ok := d["power"]; ok {
		t.Error("мощность попала в распределения по диапазонам")
	}
}

func TestMetricStrips(t *testing.T) {
	d := map[string][]distEntry{
		"hr": {
			{Label: "140–150", Secs: 10, Pct: 100},
		},
		"cadence": {
			{Label: "80–90", Secs: 5, Pct: 50},
			{Label: "90–100", Secs: 5, Pct: 50},
			{Label: "100+", Secs: 0, Pct: 0},
		},
		"speed": {
			{Label: "25–30", Secs: 10, Pct: 100},
		},
	}
	strips := metricStrips(d, false)
	if len(strips) != 3 {
		t.Fatalf("полосок %d, хочу 3", len(strips))
	}
	for i, want := range []string{"Пульс", "Каденс", "Скорость"} {
		if strips[i].Label != want {
			t.Errorf("полоска %d = %q, хочу %q", i, strips[i].Label, want)
		}
	}
	if got := len(strips[1].Entries); got != 2 {
		t.Errorf("сегментов каденса %d, хочу 2 (пустые скрыты)", got)
	}
	if strips[0].Class != "dist-hr" || strips[1].Class != "dist-cadence" || strips[2].Class != "dist-speed" {
		t.Errorf("классы полосок = %q/%q/%q", strips[0].Class, strips[1].Class, strips[2].Class)
	}

	if got := metricStrips(d, true); len(got) != 2 {
		t.Errorf("у станка полосок %d, хочу 2 (скорость скрыта)", len(got))
	}
	if got := metricStrips(nil, false); len(got) != 0 {
		t.Errorf("без данных полосок %d, хочу 0", len(got))
	}
}

// TestDistShadeClass — каждый бакет метрики получает класс по своей позиции
// в исходном срезе бакетов: первый бакет — dist-*-1, последний — dist-*-N.
func TestDistShadeClass(t *testing.T) {
	cases := []struct {
		i    int
		want string
	}{
		{0, "dist-hr-1"},
		{1, "dist-hr-2"},
		{2, "dist-hr-3"},
		{4, "dist-hr-5"},
		{6, "dist-hr-7"},
		{7, "dist-hr-8"},
	}
	for _, tt := range cases {
		if got := distShadeClass("dist-hr", tt.i); got != tt.want {
			t.Errorf("distShadeClass(dist-hr, %d) = %q, хочу %q", tt.i, got, tt.want)
		}
	}
	// для полного набора бакетов все классы разные
	seen := make(map[string]bool)
	for i := 0; i < 8; i++ {
		class := distShadeClass("dist-cadence", i)
		if seen[class] {
			t.Errorf("класс %q повторяется для разных позиций", class)
		}
		seen[class] = true
		n, err := strconv.Atoi(strings.TrimPrefix(class, "dist-cadence-"))
		if err != nil {
			t.Fatalf("класс %q не парсится: %v", class, err)
		}
		if want := i + 1; n != want {
			t.Errorf("класс на позиции %d = %d, хочу %d", i, n, want)
		}
	}
}

// TestMetricStripShadeClasses — класс сегмента считается по позиции бакета в
// исходном срезе, а не среди непустых: высокий бакет на позиции 2 получает
// dist-hr-3.
func TestMetricStripShadeClasses(t *testing.T) {
	d := map[string][]distEntry{
		"hr": {
			{Label: "0–100", Secs: 0, Pct: 0},
			{Label: "100–120", Secs: 0, Pct: 0},
			{Label: "170+", Secs: 10, Pct: 100},
		},
	}
	strips := metricStrips(d, false)
	if len(strips) != 1 {
		t.Fatalf("полосок %d, хочу 1", len(strips))
	}
	if got := strips[0].Entries[0].Class; got != "dist-hr-3" {
		t.Errorf("класс высокого бакета = %q, хочу dist-hr-3 (по позиции в срезе)", got)
	}
}

// TestWorkoutMetricLegend — под каждой полоской строк сводки рендерится
// легенда в одну строку: цветная точка + подпись диапазона + %; у мощности
// подписи Z1–Z7, у пульса/каденса/скорости — диапазоны; сегменты dist-полосок
// получают разные цветовые классы по позиции бакета; пустые сегменты (Pct 0) в легенду
// не попадают.
func TestWorkoutMetricLegend(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: 60 * time.Second,
		HasPower: true, AvgPower: 150, FTP: 200, FTPKind: "trainer",
		AvgHR: 130, MaxHR: 150, AvgCadence: 90, MaxCadence: 95,
		AvgSpeedKmh: 36, MaxSpeedKmh: 43.2,
	}, "hash-legend", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := seq(
		rep(30, store.Sample{Power: 100, HasPwr: true, HR: 110, Cadence: 85, HasCad: true, SpeedMs: 8}),
		rep(30, store.Sample{Power: 200, HasPwr: true, HR: 150, Cadence: 95, HasCad: true, SpeedMs: 12}),
	)
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d: код %d", id, rec.Code)
	}
	// html/template экранирует "+" в тексте как &#43; — для сверки возвращаем
	body := strings.ReplaceAll(rec.Body.String(), "&#43;", "+")

	if n := strings.Count(body, `class="metric-legend"`); n != 4 {
		t.Errorf("легенд %d, хочу 4 (по одной под каждой полоской)", n)
	}

	// легенда мощности: только непустые зоны, подписи Z с %
	entries, ok := zoneDistribution(samples, 200)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	for _, zr := range workoutZoneRows(entries) {
		if zr.PctNum <= 0 {
			continue
		}
		want := fmt.Sprintf(`<i class="zdot z%d"></i>Z%d %s%%`, zr.N, zr.N, zr.Pct)
		if !strings.Contains(body, want) {
			t.Errorf("нет пункта легенды мощности %q", want)
		}
	}

	// легенды распределений: точка того же цвета + диапазон + %
	for _, want := range []string{
		`<i class="zdot dist-hr-2"></i>100–120 50.0%`,
		`<i class="zdot dist-hr-6"></i>150–160 50.0%`,
		`<i class="zdot dist-cadence-6"></i>80–90 50.0%`,
		`<i class="zdot dist-cadence-7"></i>90–100 50.0%`,
		`<i class="zdot dist-speed-5"></i>25–30 50.0%`,
		`<i class="zdot dist-speed-8"></i>40+ 50.0%`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("нет пункта легенды %q", want)
		}
	}

	// пустые бакеты в легенду не попадают (ни одного «пустого» пункта)
	if n := strings.Count(body, `class="zdot `); n != 8 {
		t.Errorf("пунктов легенды %d, хочу 8 (только непустые сегменты)", n)
	}
	for _, bad := range []string{`</i>0–100 `, `</i>0–40 `, `</i>0–10 `} {
		if strings.Contains(body, bad) {
			t.Errorf("в легенде остался пустой бакет %q", bad)
		}
	}
}

// (он же импорт и /api/weeks) считают зоны одинаково.
func TestZoneTimeMatchesZoneDistribution(t *testing.T) {
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{
			Offset: int64(i), Power: []int{0, 30, 120, 170, 220, 260, 320}[i%7], HasPwr: true,
		})
	}
	const ftp = 200
	entries, ok := zoneDistribution(samples, ftp)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	zones, coast := ridedata.PowerTime(samples, ftp)
	if got := entries[0].Secs; got != int(math.Round(coast)) {
		t.Errorf("накат: zoneDistribution %d, PowerTime %d", got, int(math.Round(coast)))
	}
	for j, z := range zones {
		if got := entries[j+1].Secs; got != int(math.Round(z)) {
			t.Errorf("Z%d: zoneDistribution %d, PowerTime %d", j+1, got, int(math.Round(z)))
		}
	}
}

// TestWorkoutZonesHTMLMatchesZoneDistribution — полоска зон в строке мощности
// на сводке строится из того же zoneDistribution: метки, время и % от
// педалирования совпадают; подпись FTP — под значениями строки.
func TestWorkoutZonesHTMLMatchesZoneDistribution(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: 120 * time.Second,
		Trainer: false, HasPower: true, AvgPower: 120, NormPower: 140,
		FTP: 156, FTPKind: "trainer",
	}, "hash-zones-html", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 100, HasPwr: true})
	}
	for i := 60; i < 90; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 0, HasPwr: true})
	}
	for i := 90; i < 120; i++ {
		samples = append(samples, store.Sample{Offset: int64(i), Power: 200, HasPwr: true})
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d: код %d", id, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"распределение времени по зонам мощности", "FTP 156 Вт", "станок, уличный не задан",
		"% — от времени педалирования",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на деталке нет %q", want)
		}
	}

	entries, ok := zoneDistribution(samples, 156)
	if !ok {
		t.Fatal("нет распределения мощности")
	}
	if len(entries) != 8 {
		t.Fatalf("бакетов мощности %d, хочу 8 (накат + Z1–Z7)", len(entries))
	}
	// html/template экранирует "+" в тексте как &#43; — для сверки возвращаем.
	rows := strings.ReplaceAll(body, "&#43;", "+")
	for _, e := range entries[1:] {
		if e.Secs == 0 {
			continue // пустые зоны в стрип не попадают
		}
		want := fmt.Sprintf("data-tip=\"%s — %s (%s%%)\"", e.Label, zoneTimeLabel(e.Secs), trimFloat(e.Pct))
		if !strings.Contains(rows, want) {
			t.Errorf("нет бара зоны %q (в zoneDistribution: %+v)", want, e)
		}
	}
}

// TestWorkoutDistStripsHTML — на сводке строки пульса, каденса и скорости:
// значения слева, полоска справа; подсказка сегмента содержит диапазон,
// время и долю. У станка скорости нет — ни строки, ни полоски.
func TestWorkoutDistStripsHTML(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: 10 * time.Second,
		AvgHR: 140, MaxHR: 140, AvgCadence: 85, MaxCadence: 85, AvgSpeedKmh: 28.8,
	}, "hash-strips-html", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := seq(rep(10, store.Sample{Cadence: 85, HasCad: true, HR: 140, SpeedMs: 8}))
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d: код %d", id, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`aria-label="распределение времени: Пульс"`,
		`aria-label="распределение времени: Каденс"`,
		`aria-label="распределение времени: Скорость"`,
		"Пульс", "Каденс", "Скорость", "Средний", "Макс", "Средняя",
		"dist-hr", "dist-cadence", "dist-speed",
		`data-tip="140–150 уд/мин — 10 с (100.0%)"`,
		`data-tip="80–90 об/мин — 10 с (100.0%)"`,
		`data-tip="25–30 км/ч — 10 с (100.0%)"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на сводке нет %q", want)
		}
	}

	// станок: строки скорости нет, остальные остаются
	if err := store.SetTrainerOverride(db, id, true); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d (станок): код %d", id, rec.Code)
	}
	body = rec.Body.String()
	for _, want := range []string{"Пульс", "Каденс", "dist-hr", "dist-cadence"} {
		if !strings.Contains(body, want) {
			t.Errorf("на станке нет %q", want)
		}
	}
	for _, bad := range []string{
		`class="zone-bar dist-speed"`,
		`aria-label="распределение времени: Скорость"`,
	} {
		if strings.Contains(body, bad) {
			t.Errorf("на станке осталось %q", bad)
		}
	}
	if strings.Contains(body, "км/ч —") {
		t.Error("на станке осталась подсказка полоски скорости")
	}
	if strings.Contains(body, "28.8 км/ч") {
		t.Error("на станке осталась строка скорости")
	}

	// на странице графиков раздела «Распределения» больше нет, endpoint удалён
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d/charts", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d/charts: код %d", id, rec.Code)
	}
	body = rec.Body.String()
	for _, bad := range []string{"Распределения", "distGrid", "dist-grid"} {
		if strings.Contains(body, bad) {
			t.Errorf("на графиках осталось %q", bad)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/workout/%d/dist", id), nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/workout/%d/dist: код %d, хочу 404", id, rec.Code)
	}
}
