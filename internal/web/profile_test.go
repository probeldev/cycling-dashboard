package web

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/importer"
	"cycling-dashboard/internal/store"
)

// TestProfileKinds — профиль показывает оба вида FTP, kind валидируется,
// запись ftp_outdoor сохраняется.
func TestProfileKinds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "weight", 75, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp", 156, at); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(kind, value string) int {
		t.Helper()
		body := "kind=" + kind + "&value=" + value + "&date=2026-10-30"
		req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// пока уличного FTP нет — профиль честно говорит про fallback
	{
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /profile без уличного FTP: код %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "используется станковый") {
			t.Errorf("на /profile нет подсказки про fallback уличного FTP")
		}
	}

	if code := post("garbage", "200"); code != http.StatusBadRequest {
		t.Errorf("kind=garbage: код %d, хочу 400", code)
	}
	if code := post("ftp_outdoor", "170"); code != http.StatusOK {
		t.Errorf("kind=ftp_outdoor: код %d, хочу 200", code)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /profile: код %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"FTP (станок)", "FTP (улица)", "156", "170"} {
		if !strings.Contains(body, want) {
			t.Errorf("на /profile нет %q", want)
		}
	}
}

// TestWorkoutPageFTPKind — рядом с FTP подписан источник значения, в том числе
// fallback улицы на станковый FTP.
func TestWorkoutPageFTPKind(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		w    store.Workout
		want string
	}{
		{"улица", store.Workout{FTP: 170, FTPKind: "outdoor"}, "FTP на дату (улица)"},
		{"станок", store.Workout{Trainer: true, FTP: 156, FTPKind: "trainer"}, "FTP на дату (станок)"},
		{"fallback улицы", store.Workout{FTP: 156, FTPKind: "trainer"}, "FTP на дату (станок, уличный не задан)"},
	}
	for i, c := range cases {
		c.w.StartTime = time.Now()
		c.w.Duration = time.Hour
		c.w.HasPower = true
		c.w.AvgPower = 200
		c.w.MaxPower = 400
		id, err := store.InsertWorkout(db, &c.w, "hash-page-kind-"+strconv.Itoa(i),
			filepath.Join(t.TempDir(), "x.fit"), 1)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workout/"+strconv.FormatInt(id, 10), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: код %d", c.name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: нет подписи %q", c.name, c.want)
		}
	}
}

// TestTrainerToggleRecalculatesMetrics — переключение типа сразу пересчитывает
// IF/TSS от нового FTP (без рестарта), в обе стороны; неизвестный id — 404.
func TestTrainerToggleRecalculatesMetrics(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "ftp", 200, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp_outdoor", 250, at); err != nil {
		t.Fatal(err)
	}
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
		Duration:  time.Hour, NormPower: 140, AvgPower: 123, HasPower: true,
	}, "hash-toggle", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	toggle := func(id int64, v string) int {
		t.Helper()
		body := "value=" + v
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/workout/%d/trainer", id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	metrics := func() (int, string, float64, float64) {
		t.Helper()
		w, err := store.GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		return w.FTP, w.FTPKind, w.IF, w.TSS
	}

	ftp, kind, iff, tss := metrics()
	if ftp != 250 || kind != "outdoor" || math.Abs(iff-0.56) > 1e-9 || math.Abs(tss-31.36) > 0.01 {
		t.Fatalf("исходно: FTP=%d kind=%s IF=%v TSS=%v, хочу 250/outdoor/0.56/31.36", ftp, kind, iff, tss)
	}

	// та же валидация, что у флага теста: только 0 или 1
	for _, v := range []string{"", "2", "true"} {
		if code := toggle(id, v); code != http.StatusBadRequest {
			t.Errorf("value=%q: код %d, хочу 400", v, code)
		}
	}
	ftp, kind, iff, tss = metrics()
	if ftp != 250 || kind != "outdoor" || math.Abs(iff-0.56) > 1e-9 || math.Abs(tss-31.36) > 0.01 {
		t.Errorf("мусор в value изменил тип: FTP=%d kind=%s IF=%v TSS=%v", ftp, kind, iff, tss)
	}

	if code := toggle(id, "1"); code != http.StatusOK {
		t.Fatalf("включение станка: код %d", code)
	}
	ftp, kind, iff, tss = metrics()
	if ftp != 200 || kind != "trainer" || math.Abs(iff-0.7) > 1e-9 || math.Abs(tss-49) > 0.01 {
		t.Errorf("после станка: FTP=%d kind=%s IF=%v TSS=%v, хочу 200/trainer/0.7/49", ftp, kind, iff, tss)
	}

	if code := toggle(id, "0"); code != http.StatusOK {
		t.Fatalf("возврат на улицу: код %d", code)
	}
	ftp, kind, iff, tss = metrics()
	if ftp != 250 || kind != "outdoor" || math.Abs(iff-0.56) > 1e-9 || math.Abs(tss-31.36) > 0.01 {
		t.Errorf("обратно: FTP=%d kind=%s IF=%v TSS=%v, хочу 250/outdoor/0.56/31.36", ftp, kind, iff, tss)
	}

	if code := toggle(99999, "1"); code != http.StatusNotFound {
		t.Errorf("несуществующий id: код %d, хочу 404", code)
	}
}

// TestProfileEntryRejectsNonFinite — NaN и ±Inf не проходят разбор значения:
// иначе +Inf сохранялся бы в историю и ломал JSON-ряд профиля.
func TestProfileEntryRejectsNonFinite(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"NaN", "Inf", "+Inf", "-Inf"} {
		body := url.Values{
			"kind":  {"weight"},
			"value": {v},
			"date":  {"2026-10-30"},
		}.Encode()
		req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("value=%q: код %d, хочу 400", v, rec.Code)
		}
	}
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Errorf("нефинитные значения попали в историю: %+v", history)
	}
}

// TestProfileSeriesEncodeError — ошибка json.Encode (например, +Inf из битой
// строки профиля) — это 500 с телом, а не 200 с пустым ответом.
func TestProfileSeriesEncodeError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// литерал 1e999 SQLite хранит как +Inf; через параметр так не гарантируется
	if _, err := db.Exec(`INSERT INTO profile_history (kind, value, recorded_at)
	    VALUES ('weight', 1e999, '2026-10-30T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/profile/series?days=7", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("код %d, хочу 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ошибка кодирования JSON") {
		t.Errorf("тело ошибки: %q", rec.Body.String())
	}
}

// TestProfileHistoryFractionalFTP — история профиля показывает дробный FTP
// так же, как карточки: 156.5, а не округлённые 157 или 156.
func TestProfileHistoryFractionalFTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.AddProfileEntry(db, "ftp", 156.5, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /profile: код %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "156.5 Вт") {
		t.Error("история профиля не показывает дробный FTP 156.5")
	}
}

func TestProfileWeeklyTargetForm(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func() (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
		return rec.Code, rec.Body.String()
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	code, body := get()
	if code != http.StatusOK || !strings.Contains(body, "Цель недели") || !strings.Contains(body, "не задана") {
		t.Fatalf("GET /profile: код %d, нет формы цели", code)
	}
	if rec := post("/profile/weekly-target", "value=450"); rec.Code != http.StatusOK {
		t.Errorf("POST weekly-target: код %d: %s", rec.Code, rec.Body.String())
	}
	_, body = get()
	for _, want := range []string{"450", "вручную"} {
		if !strings.Contains(body, want) {
			t.Errorf("после сохранения нет %q", want)
		}
	}
	if rec := post("/profile/weekly-target", "value=50"); rec.Code != http.StatusBadRequest {
		t.Errorf("POST value=50: код %d, хочу 400", rec.Code)
	}
	if rec := post("/profile/weekly-target/reset", ""); rec.Code != http.StatusOK {
		t.Errorf("POST reset: код %d", rec.Code)
	}
	_, body = get()
	if !strings.Contains(body, "не задана") {
		t.Error("после сброса цель всё ещё задана")
	}
}

// TestProfileWeeklyTargetPrefill — когда авто-цель не считается, поле
// получает стартовое значение: среднее недельное TSS за 4 недели, а без
// истории — разумный дефолт. Авто-цель при этом остаётся не заданной.
func TestProfileWeeklyTargetPrefill(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /profile: код %d", rec.Code)
		}
		return rec.Body.String()
	}

	if body := get(); !strings.Contains(body, `value="300"`) {
		t.Errorf("без истории в поле цели нет стартового значения 300")
	}

	// Только текущая неделя: полных недель с нагрузкой нет, авто-цель — none.
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: time.Hour, TSS: 250,
	}, "hash-target-prefill", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `value="250"`) {
		t.Errorf("в поле цели нет среднего недельного TSS 250")
	}
}

// TestProfileWeeklyTargetDBErrorIs500 — вне диапазона — 400, а сбой БД при
// валидном значении — 500, а не «некорректная цель».
func TestProfileWeeklyTargetDBErrorIs500(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(value string) int {
		req := httptest.NewRequest(http.MethodPost, "/profile/weekly-target", strings.NewReader("value="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("50"); code != http.StatusBadRequest {
		t.Errorf("value=50: код %d, хочу 400", code)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if code := post("450"); code != http.StatusInternalServerError {
		t.Errorf("value=450 на закрытой БД: код %d, хочу 500", code)
	}
}
