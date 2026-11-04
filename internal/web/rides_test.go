package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestRidesPageUI — /rides в новом дизайне: строки без карточек, спарклайны,
// чипы, у станка время вместо км.
func TestRidesPageUI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// уличная тренировка с мощностью
	outdoorID, err := store.InsertWorkout(db, &store.Workout{
		StartTime:  time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local),
		Name:       "Утренняя",
		Duration:   90 * time.Minute,
		DistanceKm: 35,
		AvgPower:   180,
		FTP:        250,
		IF:         0.72,
		TSS:        58,
		HasPower:   true,
		HasIF:      true,
		HasTSS:     true,
	}, "hash-rides-outdoor", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	outdoorSamples := make([]store.Sample, 120)
	for i := range outdoorSamples {
		outdoorSamples[i] = store.Sample{Offset: int64(i), Power: 150 + i%40, HasPwr: true}
	}
	if err := store.InsertSamples(db, outdoorID, outdoorSamples); err != nil {
		t.Fatal(err)
	}

	// станок без мощности
	_, err = store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 31, 18, 0, 0, 0, time.Local),
		Name:      "Вечерняя",
		Duration:  75 * time.Minute,
		Trainer:   true,
	}, "hash-rides-trainer", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rides", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rides: код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if n := strings.Count(body, `class="ride"`); n != 2 {
		t.Errorf("поездок на /rides = %d, хочу 2", n)
	}
	// строки — ссылки, не карточки/div'ы
	if strings.Contains(body, `<div class="ride"`) {
		t.Error("поездка осталась div-карточкой")
	}
	// спарклайн мощности
	if !strings.Contains(body, `<svg class="ride-spark"`) {
		t.Error("нет спарклайна мощности")
	}
	// у уличной поездки — км
	if !strings.Contains(body, ">35.0<") {
		t.Error("нет дистанции уличной поездки")
	}
	// у станка — время, а не 0 км
	if !strings.Contains(body, "1:15") {
		t.Error("нет времени станка")
	}
	// чипы типа тренировки
	if !strings.Contains(body, `chip--outdoor`) {
		t.Error("нет чипа 'улица'")
	}
	if !strings.Contains(body, `chip--trainer`) {
		t.Error("нет чипа 'станок'")
	}
	// фильтр по-прежнему работает
	for _, want := range []string{`data-search="30.10.2026`, `data-search="31.10.2026`} {
		if !strings.Contains(body, want) {
			t.Errorf("нет фильтруемых данных %q", want)
		}
	}
}

// TestAutoTitleRules — детерминированные правила названий тренировок.
func TestAutoTitleRules(t *testing.T) {
	tests := []struct {
		name  string
		w     store.Workout
		zones [7]float64
		want  string
	}{
		{
			name: "пороговая работа",
			w: store.Workout{
				Name: "Интервалы", IF: 0.97, HasIF: true, TSS: 95, HasTSS: true,
				Duration: 70 * time.Minute, HasPower: true,
			},
			zones: [7]float64{100, 200, 300, 400, 500, 0, 0}, // Z4+ = 900/1500 = 60%
			want:  "Пороговая работа",
		},
		{
			name: "ровное Z2",
			w: store.Workout{
				Name: "Z2", IF: 0.68, HasIF: true, TSS: 60, HasTSS: true,
				Duration: 90 * time.Minute, HasPower: true, HasVI: true, VI: 1.03,
			},
			zones: [7]float64{100, 900, 0, 0, 0, 0, 0}, // Z2 = 90%
			want:  "Ровное Z2",
		},
		{
			name: "длинная выносливость",
			w: store.Workout{
				Name: "Долгая", IF: 0.65, HasIF: true, TSS: 150, HasTSS: true,
				Duration: 3 * time.Hour, HasPower: true,
			},
			zones: [7]float64{2000, 1000, 0, 0, 0, 0, 0}, // Z1+Z2 = 100%
			want:  "Длинная выносливость",
		},
		{
			name: "восстановление",
			w: store.Workout{
				Name: "Recovery", IF: 0.55, HasIF: true, TSS: 25, HasTSS: true,
				Duration: 40 * time.Minute, HasPower: true,
			},
			zones: [7]float64{1000, 0, 0, 0, 0, 0, 0},
			want:  "Восстановление",
		},
		{
			name:  "фоллбэк по имени",
			w:     store.Workout{Name: "Свободная поездка"},
			zones: [7]float64{},
			want:  "Свободная поездка",
		},
		{
			name:  "фоллбэк станок",
			w:     store.Workout{Name: "", Trainer: true},
			zones: [7]float64{},
			want:  "Станок",
		},
		{
			name:  "фоллбэк улица",
			w:     store.Workout{Name: ""},
			zones: [7]float64{},
			want:  "Улица",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autoTitle(&tt.w, tt.zones)
			if got != tt.want {
				t.Errorf("autoTitle() = %q, хочу %q", got, tt.want)
			}
		})
	}
}

// TestRidesSyncButton — кнопка синхронизации оформлена primary с иконкой refresh.
func TestRidesSyncButton(t *testing.T) {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rides", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rides: код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="btn btn--primary"`) {
		t.Error("кнопка синхронизации не .btn--primary")
	}
	if !strings.Contains(body, `icons.svg#refresh`) {
		t.Error("нет иконки refresh на кнопке синхронизации")
	}
}

// TestRidesEmptyState — пустой список не пустой: фраза и CTA на синхронизацию.
func TestRidesEmptyState(t *testing.T) {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rides", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rides: код %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Поездки появятся после импорта или синхронизации") {
		t.Error("пустой /rides: нет фразы ожидания")
	}
	if !strings.Contains(body, `href="#syncBtn"`) {
		t.Error("пустой /rides: нет CTA на синхронизацию")
	}
	if strings.Contains(body, `class="ride"`) {
		t.Error("пустой /rides: найдена поездка")
	}
}

// TestRidesMonthlyTSSRounded — TSS месяца округляется до целых, как в строке.
func TestRidesMonthlyTSSRounded(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local),
		Duration:  time.Hour, TSS: 143.9, HasTSS: true,
	}, "hash-rides-round", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
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
	if strings.Contains(body, "143.9 TSS") {
		t.Error("месячный TSS не округлён")
	}
	if !strings.Contains(body, "144 TSS") {
		t.Error("нет округлённого TSS месяца")
	}
}

// TestRidesYearSelection — ?year= открывает конкретный год: в списке только
// его поездки, год подсвечен в меню. Пустой и неизвестный год — свежайший
// год с данными.
func TestRidesYearSelection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, w := range []struct {
		name  string
		start time.Time
	}{
		{"Ранняя", time.Date(2025, 6, 10, 8, 0, 0, 0, time.Local)},
		{"Поздняя", time.Date(2025, 11, 20, 8, 0, 0, 0, time.Local)},
		{"Свежая", time.Date(2026, 3, 15, 9, 0, 0, 0, time.Local)},
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Name: w.name, Duration: time.Hour,
		}, fmt.Sprintf("hash-rides-year-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(url string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: код %d: %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// без параметра — самый свежий год, поездки только 2026-го
	body := get("/rides")
	if n := strings.Count(body, `class="ride"`); n != 1 {
		t.Errorf("поездок без ?year = %d, хочу 1 (только свежий год)", n)
	}
	if !strings.Contains(body, `data-search="15.03.2026`) {
		t.Error("нет поездки свежего года")
	}
	if strings.Contains(body, `data-search="10.06.2025`) {
		t.Error("прошлый год попал в дефолтный выбор")
	}
	if !strings.Contains(body, `<a href="/rides?year=2026" class="active">2026</a>`) {
		t.Error("свежий год не подсвечен в меню годов")
	}

	// ?year=2025 — только 2025-й, активен в меню
	body = get("/rides?year=2025")
	if n := strings.Count(body, `class="ride"`); n != 2 {
		t.Errorf("поездок ?year=2025 = %d, хочу 2", n)
	}
	if !strings.Contains(body, `data-search="10.06.2025`) || !strings.Contains(body, `data-search="20.11.2025`) {
		t.Error("нет обеих поездок 2025 года")
	}
	if strings.Contains(body, `data-search="15.03.2026`) {
		t.Error("свежий год попал в выбор ?year=2025")
	}
	if !strings.Contains(body, `<a href="/rides?year=2025" class="active">2025</a>`) {
		t.Error("год 2025 не подсвечен в меню годов")
	}

	// ?year=1999 отсутствует в дереве — фолбэк на свежий год
	body = get("/rides?year=1999")
	if !strings.Contains(body, `<a href="/rides?year=2026" class="active">2026</a>`) {
		t.Error("неизвестный ?year не дал фолбэк на свежий год")
	}
}

// openMonthSeg — фрагмент единственного раскрытого месяца на странице.
func openMonthSeg(t *testing.T, body string) string {
	t.Helper()
	if n := strings.Count(body, `<details class="month" open>`); n != 1 {
		t.Fatalf("раскрытых месяцев = %d, хочу 1", n)
	}
	idx := strings.Index(body, `<details class="month" open>`)
	return body[idx : idx+400]
}

// TestRidesOpenMonths — раскрытие месяца по умолчанию: в текущем году это
// текущий календарный месяц (если в нём есть поездки); если в нём пусто —
// самый поздний непустой; в прошлых годах — самый поздний непустой.
func TestRidesOpenMonths(t *testing.T) {
	now := time.Now()
	curYear, curMonth := now.Year(), now.Month()
	cur := time.Date(curYear, curMonth, 1, 8, 0, 0, 0, time.Local)
	prevMonth := time.Date(curYear, curMonth-1, 10, 8, 0, 0, 0, time.Local)

	// база 1: текущий месяц и прошлый месяц этого же года — раскрыт текущий
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, start := range []time.Time{cur, prevMonth,
		time.Date(curYear-1, time.March, 8, 8, 0, 0, 0, time.Local),
		time.Date(curYear-1, time.September, 6, 8, 0, 0, 0, time.Local),
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: start, Duration: time.Hour,
		}, fmt.Sprintf("hash-rides-open-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(url string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: код %d: %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	seg := openMonthSeg(t, get("/rides"))
	if !strings.Contains(seg, `data-search="`+cur.Format("02.01.2006")) {
		t.Errorf("раскрыт не текущий месяц: %s", seg)
	}
	if strings.Contains(seg, `data-search="`+prevMonth.Format("02.01.2006")) {
		t.Errorf("прошлый месяц попал в раскрытый: %s", seg)
	}

	// прошлый год — раскрыт самый поздний месяц с поездками (сентябрь, не март)
	seg = openMonthSeg(t, get(fmt.Sprintf("/rides?year=%d", curYear-1)))
	if !strings.Contains(seg, `data-search="`+"06.09."+fmt.Sprint(curYear-1)) {
		t.Errorf("в прошлом году раскрыл не самый поздний месяц: %s", seg)
	}
	if strings.Contains(seg, `data-search="08.03.`+fmt.Sprint(curYear-1)) {
		t.Errorf("в прошлом году раскрыл ранний месяц: %s", seg)
	}
}

// TestRidesOpenMonthFallback — текущего месяца пуст: раскрыт самый поздний
// непустой месяц, а не текущий (пустые месяцы не открываем).
func TestRidesOpenMonthFallback(t *testing.T) {
	now := time.Now()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(now.Year(), now.Month()-1, 10, 8, 0, 0, 0, time.Local),
		Duration:  time.Hour,
	}, "hash-rides-fallback", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rides", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /rides: код %d: %s", rec.Code, rec.Body.String())
	}
	seg := openMonthSeg(t, rec.Body.String())
	want := time.Date(now.Year(), now.Month()-1, 10, 8, 0, 0, 0, time.Local).Format("02.01.2006")
	if !strings.Contains(seg, `data-search="`+want) {
		t.Errorf("раскрыт не последний непустой месяц: %s", seg)
	}
}

// TestRideSamplesBatchMatchesStore — batch-загрузка сэмплов даёт те же точки,
// что и store.Samples: формула списка не меняется.
func TestRideSamplesBatchMatchesStore(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local),
		Duration:  3 * time.Hour, AvgPower: 200,
	}, "hash-rides-samples", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]store.Sample, 150)
	for i := range samples {
		samples[i] = store.Sample{Offset: int64(i), Power: 100 + i%37, HasPwr: i%11 != 0}
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}

	want, err := store.Samples(db, id, 60)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := loadRideSamples(db, []int64{id}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(batch[id], want) {
		t.Errorf("batch отличается от store.Samples: %d точек против %d", len(batch[id]), len(want))
	}
}

// TestHumanMin — «осталось ≈ N Z2» на главной показываем часами и минутами.
func TestHumanMin(t *testing.T) {
	cases := map[int]string{
		335: "5 ч 35 мин",
		60:  "1 ч 0 мин",
		45:  "45 мин",
		0:   "0 мин",
	}
	for min, want := range cases {
		if got := humanMin(min); got != want {
			t.Errorf("humanMin(%d) = %q, хочу %q", min, got, want)
		}
	}
}
