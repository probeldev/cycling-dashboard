package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/advice"
	"cycling-dashboard/internal/store"
)

func TestIFVerdictBands(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want string
	}{
		{1.05, "выше FTP — гоночная интенсивность"},
		{0.97, "почти гоночная интенсивность"},
		{0.85, "твёрдая аэробная работа"},
		{0.72, "умеренная нагрузка"},
		{0.55, "лёгкая езда"},
	} {
		if got := ifVerdict(tt.v); got != tt.want {
			t.Errorf("ifVerdict(%v) = %q, хочу %q", tt.v, got, tt.want)
		}
	}
}

func TestVIVerdictBands(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want string
	}{
		{1.11, "рвано, много разгонов"},
		{1.2, "очень рвано, много разгонов"},
		{1.07, "умеренно рваная езда"},
		{1.03, "ровная работа"},
	} {
		if got := viVerdict(tt.v); got != tt.want {
			t.Errorf("viVerdict(%v) = %q, хочу %q", tt.v, got, tt.want)
		}
	}
}

func TestTSSVerdictBands(t *testing.T) {
	for _, tt := range []struct {
		v    float64
		want string
	}{
		{200, "очень тяжёлая тренировка"},
		{120, "тяжёлая тренировка"},
		{80, "средняя нагрузка"},
		{30, "лёгкая нагрузка"},
	} {
		if got := tssVerdict(tt.v); got != tt.want {
			t.Errorf("tssVerdict(%v) = %q, хочу %q", tt.v, got, tt.want)
		}
	}
}

func TestInsightLevelMapping(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{advice.LevelWarn, "warn"},
		{advice.LevelGood, "ok"},
		{advice.LevelInfo, "info"},
	} {
		if got := insightLevel(tt.in); got != tt.want {
			t.Errorf("insightLevel(%q) = %q, хочу %q", tt.in, got, tt.want)
		}
	}
}

func TestRecordFacts(t *testing.T) {
	cards := []mmpCard{
		{Secs: 5, Label: "5 с", Watts: 640.4, PR: "рекорд за месяц", PRPeriod: "за месяц"},
		{Secs: 60, Label: "1 мин", Watts: 400}, // не рекорд — пропускаем
		{Secs: 300, Label: "5 мин", Watts: 312.6, PR: "рекорд за год", PRPeriod: "за год"},
	}
	facts := recordFacts(cards)
	if len(facts) != 2 {
		t.Fatalf("фактов %d, хочу 2", len(facts))
	}
	if facts[0] != (advice.RecordFact{Window: "5 с", Period: "за месяц", Watts: 640}) {
		t.Errorf("факт[0] = %+v", facts[0])
	}
	if facts[1] != (advice.RecordFact{Window: "5 мин", Period: "за год", Watts: 313}) {
		t.Errorf("факт[1] = %+v", facts[1])
	}
}

// TestAttachRecordTrends — тренд сравнивает значение ЭТОЙ тренировки
// (Watts карточки) с лучшим значением того же окна за прошлый период до её
// даты; карточки без данных и окна без прошлого периода тренда не получают.
func TestAttachRecordTrends(t *testing.T) {
	cards := []mmpCard{
		{Secs: 60, Label: "1 мин", Watts: 406},
		{Secs: 300, Label: "5 мин", Watts: 300},
		{Secs: 1200, Label: "20 мин", Watts: 250}, // в прошлом периоде окна нет
		{Secs: 5, Label: "5 с"},                   // карточка без данных
	}
	prev := []store.Record{{WindowSec: 60, Watts: 400}, {WindowSec: 300, Watts: 306}}
	attachRecordTrends(cards, prev)
	if cards[0].Trend != 6 {
		t.Errorf("тренд 1 мин = %d, хочу +6 (406 против 400 в прошлом периоде)", cards[0].Trend)
	}
	if cards[1].Trend != -6 {
		t.Errorf("тренд 5 мин = %d, хочу -6 (300 против 306 в прошлом периоде)", cards[1].Trend)
	}
	if cards[2].Trend != 0 {
		t.Errorf("тренд без прошлого периода = %d, хочу 0", cards[2].Trend)
	}
	if cards[3].Trend != 0 {
		t.Errorf("тренд карточки без данных = %d, хочу 0", cards[3].Trend)
	}
}

func TestZoneEdgesW(t *testing.T) {
	if got := zoneEdgesW(0); got != nil {
		t.Errorf("без FTP границ быть не должно: %v", got)
	}
	got := zoneEdgesW(200)
	if len(got) != 7 {
		t.Fatalf("границ %d, хочу 7", len(got))
	}
	if got[0] != 0 || got[1] != 112 || got[6] != 302 {
		t.Errorf("границы от FTP 200: %v", got)
	}
}

// TestWorkoutMetricRows — сборка строк сводки «значения слева, полоска
// справа»: порядок мощность/пульс/каденс/скорость; у станка строки скорости
// нет, без метрик строк нет.
func TestWorkoutMetricRows(t *testing.T) {
	street := &store.Workout{
		AvgPower: 156, NormPower: 180, MaxPower: 400, FTP: 200, FTPKind: "outdoor",
		AvgHR: 140, MaxHR: 165, AvgCadence: 66, AvgCadenceActive: 78, MaxCadence: 100,
		AvgSpeedKmh: 28.8, MaxSpeedKmh: 44.6,
	}
	zones := []workoutZoneRow{{N: 2, Label: "Z2 · 112–152", Time: "30 с", Pct: "50.0", PctNum: 50}}
	strips := []metricStrip{
		{Label: "Пульс", Unit: "уд/мин", Class: "dist-hr",
			Entries: []metricStripEntry{{Label: "140–150", Time: "30 с", Pct: "100.0", PctNum: 100}}},
		{Label: "Каденс", Unit: "об/мин", Class: "dist-cadence",
			Entries: []metricStripEntry{{Label: "70–80", Time: "30 с", Pct: "100.0", PctNum: 100}}},
		{Label: "Скорость", Unit: "км/ч", Class: "dist-speed",
			Entries: []metricStripEntry{{Label: "25–30", Time: "30 с", Pct: "100.0", PctNum: 100}}},
	}

	rows := workoutMetricRows(street, zones, strips)
	if len(rows) != 4 {
		t.Fatalf("строк %d, хочу 4 (мощность, пульс, каденс, скорость)", len(rows))
	}
	for i, want := range []string{"Мощность", "Пульс", "Каденс", "Скорость"} {
		if rows[i].Label != want {
			t.Errorf("строка %d = %q, хочу %q", i, rows[i].Label, want)
		}
	}
	pwr := rows[0]
	if pwr.AvgName != "NP" || pwr.AvgVal != "180 Вт" || pwr.AvgNote != "средняя с весом пиков" {
		t.Errorf("мощность: средняя = %q/%q/%q", pwr.AvgName, pwr.AvgVal, pwr.AvgNote)
	}
	if pwr.MaxVal != "400 Вт" {
		t.Errorf("мощность: макс = %q, хочу %q", pwr.MaxVal, "400 Вт")
	}
	if pwr.Footer != "FTP 200 Вт — улица; % — от времени педалирования" {
		t.Errorf("мощность: подпись FTP = %q", pwr.Footer)
	}
	if len(pwr.Strip) != 1 || pwr.Strip[0].Class != "z2" || pwr.Strip[0].Title != "Z2 · 112–152 — 30 с (50.0%)" {
		t.Errorf("мощность: полоска = %+v", pwr.Strip)
	}
	if rows[2].AvgVal != "78" || rows[2].AvgNote != "66 с накатом" {
		t.Errorf("каденс: средний = %q/%q", rows[2].AvgVal, rows[2].AvgNote)
	}
	if rows[3].AvgVal != "28.8 км/ч" || rows[3].MaxVal != "44.6 км/ч" {
		t.Errorf("скорость: %q/%q", rows[3].AvgVal, rows[3].MaxVal)
	}
	if rows[3].StripAria != "распределение времени: Скорость" {
		t.Errorf("скорость: aria = %q", rows[3].StripAria)
	}

	// станок: строки скорости нет, остальные остаются
	trainer := *street
	trainer.Trainer = true
	if rows := workoutMetricRows(&trainer, nil, strips[:2]); len(rows) != 3 {
		t.Fatalf("у станка строк %d, хочу 3 (без скорости)", len(rows))
	}

	// NP не считался — слева средняя мощность, без подписи про пики
	avgOnly := *street
	avgOnly.NormPower = 0
	if rows := workoutMetricRows(&avgOnly, nil, nil); rows[0].AvgName != "Средняя" || rows[0].AvgNote != "" {
		t.Errorf("без NP: средняя = %q/%q", rows[0].AvgName, rows[0].AvgNote)
	}

	// «в работе» не считался — основной каденс общий, подпись про накат не нужна
	fallback := *street
	fallback.AvgCadenceActive = 0
	if rows := workoutMetricRows(&fallback, nil, nil); rows[2].AvgVal != "66" || rows[2].AvgNote != "" {
		t.Errorf("каденс без «в работе» = %q/%q", rows[2].AvgVal, rows[2].AvgNote)
	}

	// тренировка без пульса — строки пульса нет
	noHR := *street
	noHR.AvgHR, noHR.MaxHR = 0, 0
	for _, r := range workoutMetricRows(&noHR, nil, nil) {
		if r.Label == "Пульс" {
			t.Error("без пульса собралась строка пульса")
		}
	}

	// без метрик строк нет вовсе
	if rows := workoutMetricRows(&store.Workout{}, nil, nil); len(rows) != 0 {
		t.Errorf("строк %d, хочу 0", len(rows))
	}
}

// TestWorkoutPageInsightsCollapsed — на деталке видно не больше 3 инсайтов,
// остальные — под кнопкой «ещё» (§4.3), а не простынёй.
func TestWorkoutPageInsightsCollapsed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	// вчерашняя тяжёлая тренировка включает правило «два тяжёлых дня подряд»
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 90, HasTSS: true,
	}, "hash-insights-yesterday", filepath.Join(t.TempDir(), "y.fit"), 1); err != nil {
		t.Fatal(err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-2 * time.Hour), Duration: time.Hour,
		AvgCadence: 60, AvgCadenceActive: 60, // низкий каденс
		AvgPower: 150, NormPower: 180, HasPower: true,
		IF: 0.97, HasIF: true, // микро-вывод «почти гоночная»
		VI: 1.2, HasVI: true, TSS: 120, HasTSS: true, // рваная + тяжёлая
	}, "hash-insights-today", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if n := strings.Count(body, `class="insight insight--`); n != 4 {
		t.Errorf("инсайтов %d, хочу 4 (3 + 1 под «ещё»)", n)
	}
	for _, want := range []string{"<details class=\"insights-more\">", "ещё 1", "Рваная езда", "Два тяжёлых дня подряд", "Низкий каденс"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет %q", want)
		}
	}
	// у каждой аббревиатуры — микро-вывод смысла (§8.5)
	for _, want := range []string{"почти гоночная", "рвано", "средняя с весом пиков", "ровность"} {
		if !strings.Contains(body, want) {
			t.Errorf("нет микро-вывода %q", want)
		}
	}
}

// TestWorkoutPagesSeparate — «Сводка», «Графики» и «Карта» — отдельные
// страницы: боковое меню ссылается на /workout/{id}, /workout/{id}/charts и
// /workout/{id}/map (без якорей); графики живут только на своей странице,
// Leaflet — только на карте. Без GPS пункта «Карта» в меню нет.
func TestWorkoutPagesSeparate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
		AvgPower: 150, NormPower: 180, MaxPower: 400, FTP: 200, HasPower: true,
	}, "hash-pages", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	summaryPath := fmt.Sprintf("/workout/%d", id)
	code, body := get(summaryPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: код %d", summaryPath, code)
	}
	for _, want := range []string{
		fmt.Sprintf(`<a href="/workout/%d" class="active">Сводка</a>`, id),
		fmt.Sprintf(`<a href="/workout/%d/charts">Графики</a>`, id),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: нет навигации %q", summaryPath, want)
		}
	}
	for _, bad := range []string{`href="#summary"`, `href="#charts"`, "chart.umd.min.js", "leaflet", `id="map"`} {
		if strings.Contains(body, bad) {
			t.Errorf("GET %s: остался %q", summaryPath, bad)
		}
	}
	if strings.Contains(body, fmt.Sprintf(`href="/workout/%d/map"`, id)) {
		t.Errorf("GET %s: без GPS в меню не должно быть «Карты»", summaryPath)
	}

	chartsPath := fmt.Sprintf("/workout/%d/charts", id)
	code, body = get(chartsPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: код %d", chartsPath, code)
	}
	for _, want := range []string{
		`<canvas id="chart"></canvas>`,
		"/static/vendor/chart.umd.min.js",
		fmt.Sprintf(`<a href="/workout/%d">Сводка</a>`, id),
		fmt.Sprintf(`<a href="/workout/%d/charts" class="active">Графики</a>`, id),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: нет %q", chartsPath, want)
		}
	}
	for _, bad := range []string{`href="#`, "leaflet", "scroll-spy", fmt.Sprintf(`href="/workout/%d/map"`, id)} {
		if strings.Contains(body, bad) {
			t.Errorf("GET %s: лишнее %q", chartsPath, bad)
		}
	}

	for _, path := range []string{"/workout/999999", "/workout/999999/charts", "/workout/999999/map"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("GET %s: код %d, хочу 404", path, code)
		}
	}
}

// TestWorkoutMapPage — «Карта» — отдельная страница: Leaflet и #map живут
// только там, пункт меню появляется только у уличной тренировки с треком.
// Станку карта недоступна, даже если в сэмплах есть координаты (дрейф).
func TestWorkoutMapPage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	insertWorkout := func(hash string, w store.Workout) int64 {
		t.Helper()
		id, err := store.InsertWorkout(db, &w, hash, filepath.Join(t.TempDir(), hash+".fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	gpsID := insertWorkout("hash-map-gps", store.Workout{
		StartTime: time.Now(), Duration: time.Hour, DistanceKm: 20,
	})
	if err := store.InsertSamples(db, gpsID, []store.Sample{
		{Offset: 0, Lat: 55.75, Lon: 37.61, HasPos: true},
		{Offset: 60, Lat: 55.76, Lon: 37.62, HasPos: true},
	}); err != nil {
		t.Fatal(err)
	}

	// станок с «GPS»-сэмплами: координаты дрейфуют на ~100–200 м, трека нет
	trainerID := insertWorkout("hash-map-trainer", store.Workout{
		StartTime: time.Now(), Duration: time.Hour, Trainer: true,
	})
	if err := store.InsertSamples(db, trainerID, []store.Sample{
		{Offset: 0, Lat: 55.7500, Lon: 37.6100, HasPos: true},
		{Offset: 60, Lat: 55.7510, Lon: 37.6100, HasPos: true},
		{Offset: 120, Lat: 55.7500, Lon: 37.6110, HasPos: true},
	}); err != nil {
		t.Fatal(err)
	}

	// станок без сэмплов — то же сообщение
	trainerEmptyID := insertWorkout("hash-map-trainer-empty", store.Workout{
		StartTime: time.Now(), Duration: time.Hour, Trainer: true,
	})

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	assertNoMap := func(label string, id int64) {
		t.Helper()
		for _, path := range []string{
			fmt.Sprintf("/workout/%d", id),
			fmt.Sprintf("/workout/%d/charts", id),
		} {
			code, body := get(path)
			if code != http.StatusOK {
				t.Fatalf("%s GET %s: код %d", label, path, code)
			}
			if strings.Contains(body, fmt.Sprintf(`href="/workout/%d/map"`, id)) {
				t.Errorf("%s GET %s: в меню не должно быть «Карты»", label, path)
			}
		}
		mapPath := fmt.Sprintf("/workout/%d/map", id)
		code, body := get(mapPath)
		if code != http.StatusOK {
			t.Fatalf("%s GET %s: код %d", label, mapPath, code)
		}
		if !strings.Contains(body, "Карта недоступна для тренировок на станке") {
			t.Errorf("%s GET %s: нет сообщения про станок", label, mapPath)
		}
		for _, bad := range []string{"leaflet", `id="map"`} {
			if strings.Contains(body, bad) {
				t.Errorf("%s GET %s: не должно быть %q", label, mapPath, bad)
			}
		}
	}
	assertNoMap("станок с дрейфом", trainerID)
	assertNoMap("станок без сэмплов", trainerEmptyID)

	mapLink := fmt.Sprintf(`<a href="/workout/%d/map">Карта</a>`, gpsID)
	activeMapLink := fmt.Sprintf(`<a href="/workout/%d/map" class="active">Карта</a>`, gpsID)

	summaryPath := fmt.Sprintf("/workout/%d", gpsID)
	code, body := get(summaryPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: код %d", summaryPath, code)
	}
	if !strings.Contains(body, mapLink) {
		t.Errorf("GET %s: в меню нет пункта «Карта»", summaryPath)
	}
	for _, bad := range []string{"leaflet", `id="map"`} {
		if strings.Contains(body, bad) {
			t.Errorf("GET %s: сводка не должна содержать %q", summaryPath, bad)
		}
	}

	chartsPath := fmt.Sprintf("/workout/%d/charts", gpsID)
	code, body = get(chartsPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: код %d", chartsPath, code)
	}
	if !strings.Contains(body, mapLink) {
		t.Errorf("GET %s: в меню нет пункта «Карта»", chartsPath)
	}
	if strings.Contains(body, "leaflet") {
		t.Errorf("GET %s: Leaflet должен быть только на карте", chartsPath)
	}

	mapPath := fmt.Sprintf("/workout/%d/map", gpsID)
	code, body = get(mapPath)
	if code != http.StatusOK {
		t.Fatalf("GET %s: код %d", mapPath, code)
	}
	for _, want := range []string{
		`/static/vendor/leaflet.js`,
		`/static/vendor/leaflet.css`,
		`id="map"`,
		activeMapLink,
		"fetch(`/api/workout/${id}/samples`)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s: нет %q", mapPath, want)
		}
	}
}

// TestWorkoutPageNamedHeader — тренировка С названием рендерит именную ветку
// шапки: <span class="w-name">· Название</span> внутри h1, рядом карандаш
// переименования; дубль редактирования названия в настройках убран (§4.3).
func TestWorkoutPageNamedHeader(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-name-header", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	// InsertWorkout имя не пишет — это пользовательское поле, переживает ресинк
	if err := store.SetName(db, id, "Название"); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="w-name">· Название</span>`) {
		t.Error("именная ветка шапки не отрендерилась")
	}
	if !strings.Contains(body, `icons.svg#pencil`) {
		t.Error("в шапке нет карандаша переименования")
	}
	if strings.Contains(body, `id="wname"`) {
		t.Error("в настройках остался дублирующий редактор названия")
	}
}

// TestWorkoutPageTypeToggle — тип тренировки переключается из шапки: карандаш
// у чипа открывает поповер с опциями улица/станок, метка «выбран вручную»
// живёт там же; дублирующая строка из «Параметров» убрана (§4.3).
func TestWorkoutPageTypeToggle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour,
	}, "hash-type-toggle", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("код %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get()
	for _, want := range []string{`id="wTypeBtn"`, `id="wTypePop"`, `chip chip--outdoor`, `data-value="0"`, `data-value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("в шапке нет контрола типа %q", want)
		}
	}
	if strings.Contains(body, "Тип тренировки") {
		t.Error("в «Параметрах» осталась строка переключения типа")
	}
	if strings.Contains(body, "выбран вручную") {
		t.Error("без override метки «выбран вручную» быть не должно")
	}

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/workout/%d/trainer", id), strings.NewReader("value=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST trainer: код %d", rec.Code)
	}
	body = get()
	if !strings.Contains(body, `chip chip--trainer`) {
		t.Error("после переключения чип «станок» не отрендерился")
	}
	if !strings.Contains(body, "выбран вручную") {
		t.Error("после ручного переключения в шапке нет метки «выбран вручную»")
	}
}
