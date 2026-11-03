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
