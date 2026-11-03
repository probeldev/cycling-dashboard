package web

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestTSBFormat — один знак после запятой, без «-0.0», подпись по округлённому
// значению согласована с порогами.
func TestTSBFormat(t *testing.T) {
	for _, tt := range []struct {
		in    float64
		text  string
		label string
	}{
		{-0.04, "0.0", "норма"},
		{-9.976, "−10.0", "норма"}, // ровно -10 ещё «норма», порог строгий
		{-10.14, "−10.1", "загружен"},
		{-25.14, "−25.1", "перегружен"},
		{5.06, "5.1", "норма"},
		{10.06, "10.1", "свежий"},
	} {
		v := round1(tt.in)
		if got := format1(v); got != tt.text {
			t.Errorf("format1(%v) = %q, хочу %q", tt.in, got, tt.text)
		}
		if got := tsbLabel(v); got != tt.label {
			t.Errorf("tsbLabel(%v) = %q, хочу %q", tt.in, got, tt.label)
		}
	}
}

// TestProgressCards — карточка «TSS за 4 недели» включает текущую неделю.
func TestProgressCards(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	ws := store.WeekStart(now)
	today := store.StartOfDay(now)
	for _, w := range []struct {
		start time.Time
		tss   float64
		hash  string
	}{
		{today, 100, "hash-prog-cur"},
		{ws.AddDate(0, 0, -7).Add(12 * time.Hour), 50, "hash-prog-prev"},
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, TSS: w.tss,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ">150</div><div class=\"k\">TSS за 4 недели") {
		t.Errorf("карточка 4 недель не включает текущую (жду 150)")
	}
	if !strings.Contains(body, ">100</div><div class=\"k\">TSS за неделю") {
		t.Errorf("карточка недели не показывает 100")
	}

	// число и подпись TSB согласованы между собой; PMC — на конец прошлого дня
	days, err := store.TimelineUntil(db, today.AddDate(0, 0, -1), today, now)
	if err != nil {
		t.Fatal(err)
	}
	pmcDay := days[len(days)-1]
	if len(days) >= 2 {
		pmcDay = days[len(days)-2]
	}
	tsb := round1(round1(pmcDay.CTL) - round1(pmcDay.ATL))
	if !strings.Contains(body, ">"+format1(tsb)+"</div><div class=\"k\">TSB · "+tsbLabel(tsb)) {
		t.Errorf("карточка TSB не совпадает с расчётом %v", tsb)
	}
}

// TestProgressPMCWatchesStatus — карточка PMC на /progress показывает тот же
// момент, что и /api/status: конец предыдущего дня. Иначе CTL−ATL не равно TSB
// (сегодняшние CTL/ATL смешивались бы со свежестью до нагрузки сегодня).
func TestProgressPMCWatchesStatus(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	today := store.StartOfDay(now)
	for i := 1; i <= 3; i++ {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: today.AddDate(0, 0, -i).Add(12 * time.Hour),
			Duration:  time.Hour,
			TSS:       70,
		}, fmt.Sprintf("hash-progress-pmc-%d", i), filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	status, err := buildStatus(db, now, false)
	if err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	for _, want := range []string{
		">" + trimFloat(status.State.CTL.Value) + "</div><div class=\"k\">CTL · форма",
		">" + trimFloat(status.State.ATL.Value) + "</div><div class=\"k\">ATL · усталость",
		">" + format1(status.State.TSB.Value) + "</div><div class=\"k\">TSB · " +
			tsbLabel(status.State.TSB.Value),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на /progress нет %q", want)
		}
	}
	if diff := math.Abs(status.State.CTL.Value - status.State.ATL.Value - status.State.TSB.Value); diff > 0.1 {
		t.Errorf("CTL−ATL−TSB = %.1f, хочу 0", diff)
	}
}

// TestProgressBikeStats — блок велосипедов: часы включают станок, километры —
// только реальную дистанцию (у станковой тренировки distance = 0).
func TestProgressBikeStats(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bikes, err := store.Bikes(db)
	if err != nil || len(bikes) == 0 {
		t.Fatalf("велосипеды: %v, %v", bikes, err)
	}
	bike := bikes[0].ID
	for _, w := range []struct {
		hash    string
		km      float64
		dur     time.Duration
		trainer bool
	}{
		{"hash-progress-bike-road", 30, 2 * time.Hour, false},
		{"hash-progress-bike-trainer", 0, time.Hour, true},
	} {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: time.Now(), Duration: w.dur, DistanceKm: w.km, Trainer: w.trainer,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetWorkoutBike(db, id, &bike); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Пробег по велосипедам") {
		t.Fatalf("нет блока велосипедов")
	}
	want := "<td>" + bikes[0].Name + "</td><td>2</td><td>30.0</td><td>3.0</td>"
	if !strings.Contains(body, want) {
		t.Errorf("строка велосипеда не совпала: хочу %q", want)
	}
}

// TestProgressFourWeekBounds — «4 недели» — ровно 28 календарных дней:
// today−27 входит, today−28 уже нет; знаменатель coverage тоже 28.
func TestProgressFourWeekBounds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	today := store.StartOfDay(time.Now())
	for _, w := range []struct {
		start time.Time
		tss   float64
		hash  string
	}{
		{today, 10, "hash-b-cur"}, // сегодня
		{today.AddDate(0, 0, -27).Add(12 * time.Hour), 20, "hash-b-edge-in"},  // 28-й день — входит
		{today.AddDate(0, 0, -28).Add(12 * time.Hour), 40, "hash-b-edge-out"}, // день до окна — не входит
	} {
		if _, err := store.InsertWorkout(db, &store.Workout{
			StartTime: w.start, Duration: time.Hour, TSS: w.tss,
		}, w.hash, filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	// 10 + 20 = 30: окно ровно 28 дней, без today−28
	if !strings.Contains(body, ">30</div><div class=\"k\">TSS за 4 недели") {
		t.Errorf("границы 4 недель неверные (жду 30, без 40)")
	}
	if !strings.Contains(body, `class="coverage">2 дня из 28`) {
		t.Errorf("coverage 4 недель: нет «2 дня из 28»")
	}
}

// TestBuildPMCTrend — тренд PMC: стрелка по направлению изменения, цвет по
// пользе направления (рост ATL — красный), нулевые изменения не рисуем.
func TestBuildPMCTrend(t *testing.T) {
	for _, tt := range []struct {
		name      string
		cur, prev float64
		upIsGood  bool
		wantNil   bool
		text      string
		class     string
	}{
		{"CTL растёт", 52.3, 50, true, false, "▲ 2.3 за 7 дн", "up"},
		{"CTL падает", 48, 50, true, false, "▼ 2.0 за 7 дн", "down"},
		{"ATL растёт", 60, 55, false, false, "▲ 5.0 за 7 дн", "down"},
		{"ATL падает", 40, 45, false, false, "▼ 5.0 за 7 дн", "up"},
		{"без изменений", 50.04, 50, true, true, "", ""},
	} {
		got := buildPMCTrend(tt.cur, tt.prev, tt.upIsGood)
		if tt.wantNil {
			if got != nil {
				t.Errorf("%s: тренд %+v, хочу nil", tt.name, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s: тренд nil, хочу %q", tt.name, tt.text)
			continue
		}
		if got.Text != tt.text || got.Class != tt.class {
			t.Errorf("%s: %+v, хочу %q / %q", tt.name, got, tt.text, tt.class)
		}
	}
}

// TestCenturyInsight — сухой ноль сотен дополняется оценкой: километры до
// сотни и часы в Z2 по текущему уличному темпу.
func TestCenturyInsight(t *testing.T) {
	r := store.ReadinessStats{LongestEver: &store.LongestRide{KM: 60}}
	got := centuryInsight(r, 250, 10) // 25 км/ч
	if !strings.Contains(got, "~40 км") || !strings.Contains(got, "1.6 ч") {
		t.Errorf("centuryInsight = %q, хочу ~40 км и 1.6 ч", got)
	}
	if got := centuryInsight(store.ReadinessStats{CenturyCount: 1}, 250, 10); got != "" {
		t.Errorf("при наличии сотни инсайт не нужен, получили %q", got)
	}
	if got := centuryInsight(store.ReadinessStats{}, 0, 0); !strings.Contains(got, "регулярными выездами") {
		t.Errorf("без поездок жду подсказку про регулярные выезды, получили %q", got)
	}
}

// TestOutdoorRolling28IgnoresTrainer — станок в окне не раздувает часы для
// темпа «первой сотни»: скорость считается только по outdoor-км и часам.
func TestOutdoorRolling28IgnoresTrainer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-3 * time.Hour), Duration: 10 * time.Hour,
		DistanceKm: 250,
	}, "hash-century-outdoor", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: now.Add(-time.Hour), Duration: 2 * time.Hour, Trainer: true,
	}, "hash-century-trainer", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}

	// Readiness.Rolling28 смешивает км улицы и часы станка — так считать нельзя.
	r, err := store.Readiness(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rolling28.KM != 250 || r.Rolling28.Hours != 12 {
		t.Fatalf("подготовка: rolling28 = %+v, хочу 250 км / 12 ч", r.Rolling28)
	}

	km, hours, err := outdoorRolling28(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if km != 250 || hours != 10 {
		t.Fatalf("outdoorRolling28 = %.1f км / %.1f ч, хочу 250 / 10", km, hours)
	}
	got := centuryInsight(store.ReadinessStats{LongestEver: &store.LongestRide{KM: 60}}, km, hours)
	if !strings.Contains(got, "1.6 ч") {
		t.Errorf("centuryInsight = %q, хочу 1.6 ч при уличном темпе 25 км/ч", got)
	}
}

// TestProgressRedesign — якорная навигация, coverage-пометки окон TSS и
// инсайт к нулю сотен на месте (TODO-UI §4.4).
func TestProgressRedesign(t *testing.T) {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/progress", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="progress-nav"`,
		`href="#summary"`, `href="#load"`, `href="#power"`, `href="#distance"`,
		`class="coverage">0 дней из 7`,
		`class="coverage">0 дней из 28`,
		`class="coverage">0 дней из 90`,
		"Мало данных: 0 дней с нагрузкой из 42",
		"Кривая PMC появится после 2–3 тренировок",
		"Кривая MMP появится после 2–3 тренировок",
		"Зоны появятся после 2–3 тренировок",
		"Первая сотня", "Сотенных поездок пока не было",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("на /progress нет %q", want)
		}
	}
	if strings.Contains(body, "←") {
		t.Error("в заголовке страницы осталась стрелка назад")
	}
}
