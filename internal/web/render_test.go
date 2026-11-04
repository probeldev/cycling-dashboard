package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestRenderPages — все HTML-страницы рендерятся целиком с реальными данными:
// «Сегодня», деталка и профиль; дерево поездок живёт на /rides.
func TestRenderPages(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime:  time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local),
		Duration:   90 * time.Minute,
		DistanceKm: 27.8,
		AvgPower:   123,
		MaxPower:   436,
		NormPower:  140,
		FTP:        156,
		IF:         0.8974,
		TSS:        118.17,
		VI:         1.138,
		HasPower:   true,
		HasIF:      true,
		HasTSS:     true,
		HasVI:      true,
		Calories:   900,
		ElapsedSec: 5700,
		PauseSec:   300,
		AvgHR:      140,
		MaxHR:      165,
		AvgCadence: 85,
		MaxCadence: 100,
		AvgSpeedKmh: 28.8,
	}, "hash-render", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	// пульс/каденс/скорость/мощность сэмплами — чтобы на сводке были полоски
	var samples []store.Sample
	for i := 0; i < 60; i++ {
		samples = append(samples, store.Sample{
			Offset: int64(i), Power: 100, HasPwr: true,
			HR: 140, Cadence: 85, HasCad: true, SpeedMs: 8,
		})
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
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

	code, body := get("/")
	if code != http.StatusOK {
		t.Fatalf("GET /: код %d", code)
	}
	if !strings.Contains(body, "</html>") || strings.Contains(body, "template:") {
		t.Errorf("GET /: тело обрезано или содержит ошибку шаблона")
	}
	// на дашборде дробные TSS округляются до целых (TODO-UI §4.1)
	if !strings.Contains(body, "TSS <b>118</b>") {
		t.Errorf("GET /: в строке тренировки нет округлённого TSS")
	}

	code, body = get(fmt.Sprintf("/workout/%d", id))
	if code != http.StatusOK || !strings.Contains(body, "Сводка") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /workout: код %d, тело %d байт", code, len(body))
	}
	// заголовок секции «Сводка» убрали с деталки, nav-ссылка остаётся
	if strings.Contains(body, `<h2 class="sec-h">Сводка</h2>`) {
		t.Error("GET /workout: остался заголовок секции «Сводка»")
	}
	// плитки: Дистанция (улица), Время, Калории, TSS; IF и NP — не плитки
	for _, want := range []string{">Дистанция<", ">Время<", ">Калории<", "TSS — нагрузка"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /workout: нет плитки %q", want)
		}
	}
	if strings.Contains(body, "IF — доля от FTP</div><div class=\"verdict\">") {
		t.Error("GET /workout: IF остался плиткой с вердиктом")
	}
	// строки «значения слева, полоска справа»: мощность с зонами,
	// пульс/каденс/скорость с распределениями
	for _, want := range []string{
		`aria-label="распределение времени по зонам мощности"`,
		`aria-label="распределение времени: Пульс"`,
		`aria-label="распределение времени: Каденс"`,
		`aria-label="распределение времени: Скорость"`,
		`data-tip="140–150 уд/мин — 1 мин (100.0%)"`,
		`class="zone-bar z2"`,
		`id="stripTip"`,
		"NP", "средняя с весом пиков", ">436 Вт<", "FTP 156 Вт",
		">Средний<", ">Макс<", ">Средняя<", "28.8 км/ч",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /workout: нет строки метрик %q", want)
		}
	}
	// отдельные секции полосок больше нет
	if strings.Contains(body, "Зоны мощности") || strings.Contains(body, "Распределение времени") {
		t.Error("GET /workout: остались секции «Зоны мощности»/«Распределение времени»")
	}
	// остальные параметры — под «Больше данных»
	for _, want := range []string{
		"Больше данных", "общее время", ">паузы<", "VI — ровность", "IF — доля от FTP",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /workout: нет «Больше данных» %q", want)
		}
	}

	code, body = get(fmt.Sprintf("/workout/%d/charts", id))
	if code != http.StatusOK || !strings.Contains(body, `<canvas id="chart"></canvas>`) || !strings.Contains(body, "</html>") {
		t.Errorf("GET /workout/charts: код %d, тело %d байт", code, len(body))
	}

	// без GPS-сэмплов карта отдаёт сообщение, а Leaflet на страницу не тянется
	code, body = get(fmt.Sprintf("/workout/%d/map", id))
	if code != http.StatusOK || !strings.Contains(body, "У этой тренировки нет GPS-трека") ||
		strings.Contains(body, "leaflet") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /workout/map: код %d, тело %d байт", code, len(body))
	}

	code, body = get("/profile")
	if code != http.StatusOK || !strings.Contains(body, "Профиль") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /profile: код %d, тело %d байт", code, len(body))
	}

	code, body = get("/progress")
	if code != http.StatusOK || !strings.Contains(body, "CTL") || !strings.Contains(body, "heatmap") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /progress: код %d, тело %d байт", code, len(body))
	}
}

// TestRenderErrorReturns500 — ошибка шаблона даёт 500, а не 200 с мусором.
func TestRenderErrorReturns500(t *testing.T) {
	rec := httptest.NewRecorder()
	render(rec, "no-such-template.html", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("код %d, хочу 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "</html>") {
		t.Error("в ответе остался HTML")
	}
}

func TestRenderTodayAndRidesRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().AddDate(0, 0, -1), Duration: time.Hour, TSS: 80,
		Trainer: true, AvgPower: 150, AvgCadence: 85,
	}, "hash-today-rides", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	code, body := get("/")
	if code != http.StatusOK || !strings.Contains(body, "</html>") {
		t.Fatalf("GET /: код %d, тело %d байт", code, len(body))
	}
	for _, want := range []string{"Сегодня", "Рекомендация", "Форма (CTL)", "Усталость (ATL)",
		"Свежесть (TSB)", "Последняя тренировка", "Неделя", "Готовность", "Поездки",
		`class="section-label"`, "metric-card", "coverage", "Почему так"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /: нет блока %q", want)
		}
	}
	for _, bad := range []string{"NaN", "<no value>", "<b>0 Вт</b>", ">0 Вт<"} {
		if strings.Contains(body, bad) {
			t.Errorf("GET /: в вёрстке %q", bad)
		}
	}

	code, body = get("/rides")
	if code != http.StatusOK || !strings.Contains(body, "Поездки") || !strings.Contains(body, "</html>") {
		t.Errorf("GET /rides: код %d, тело %d байт", code, len(body))
	}
}

func TestRenderEmptyToday(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Мало данных", "Цель не задана", "Тренировок пока нет", "появится после первой поездки за 90 дней"} {
		if !strings.Contains(body, want) {
			t.Errorf("пустая главная: нет %q", want)
		}
	}
	if strings.Contains(body, `<svg class="spark"`) {
		t.Error("пустая главная: спарклайн без истории")
	}
	for _, bad := range []string{"NaN", "<no value>", "0 из 0"} {
		if strings.Contains(body, bad) {
			t.Errorf("пустая главная: в вёрстке %q", bad)
		}
	}
}

func TestRenderTodayWithTargetAndFTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if err := store.AddProfileEntry(db, "ftp", 200, now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.AddDate(0, 0, -1), Duration: time.Hour, TSS: 100,
		Trainer: true, AvgPower: 150,
	}, "hash-today-target", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWeeklyTarget(db, 300); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"300", "вручную", "осталось", "≈", "110–150"} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / с целью: нет %q", want)
		}
	}
}

// TestRidesPageCountAndFilterData — /rides показывает все поездки дерева и
// кладёт в data-search имя и дату для клиентского фильтра.
func TestRidesPageCountAndFilterData(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, w := range []struct {
		name  string
		start time.Time
	}{
		{"Утренняя", time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local)},
		{"Вечерняя", time.Date(2026, 10, 31, 18, 0, 0, 0, time.Local)},
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Name: w.name, Duration: time.Hour, DistanceKm: 20,
		}, fmt.Sprintf("hash-rides-page-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rides", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rides: код %d", rec.Code)
	}
	body := rec.Body.String()
	if n := strings.Count(body, `class="ride"`); n != 2 {
		t.Errorf("поездок на /rides = %d, хочу 2", n)
	}
	for _, want := range []string{`data-search="30.10.2026`, `data-search="31.10.2026`} {
		if !strings.Contains(body, want) {
			t.Errorf("нет фильтруемых данных %q", want)
		}
	}
}

// TestWorkoutCadenceActive — в строке каденса основной цифрой средний «в
// работе», общий средний подписан мелко «с накатом»; в «Больше данных»
// каденса больше нет.
func TestWorkoutCadenceActive(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: time.Hour,
		AvgCadence: 66, AvgCadenceActive: 78,
	}, "hash-cadence-render", filepath.Join(t.TempDir(), "x.fit"), 1)
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
	for _, want := range []string{">78<", "66 с накатом", "Средний"} {
		if !strings.Contains(body, want) {
			t.Errorf("на деталке нет %q", want)
		}
	}
	for _, bad := range []string{"каденс в работе", "каденс макс"} {
		if strings.Contains(body, bad) {
			t.Errorf("в «Больше данных» остался %q", bad)
		}
	}
}
