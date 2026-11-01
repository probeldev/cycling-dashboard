package web

import (
	"database/sql"
	"encoding/json"
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

// ftpTestServer — пустая база и готовый обработчик: профиль тест добавляет сам.
func ftpTestServer(t *testing.T) (*sql.DB, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return db, h
}

// ftpHintServer — база с профилем и готовый обработчик для /api/ftp-hint.
func ftpHintServer(t *testing.T, profile map[string]float64) (*sql.DB, http.Handler) {
	t.Helper()
	db, h := ftpTestServer(t)
	at := time.Now().AddDate(0, 0, -30)
	for kind, value := range profile {
		if err := store.AddProfileEntry(db, kind, value, at); err != nil {
			t.Fatal(err)
		}
	}
	return db, h
}

// addHintWorkout — тренировка с одним окном MMP 1200 с (или без него).
func addHintWorkout(t *testing.T, db *sql.DB, trainer bool, start time.Time, watts float64) int64 {
	t.Helper()
	w := &store.Workout{StartTime: start, Duration: time.Hour, Trainer: trainer}
	if watts <= 0 {
		return 0
	}
	id, err := store.InsertWorkout(db, w,
		fmt.Sprintf("hash-hint-%v-%d", trainer, start.UnixNano()), filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	recs := []store.PowerRecord{{WindowSec: 1200, Watts: watts}}
	if err := store.SetWorkoutMMP(db, id, recs, 1); err != nil {
		t.Fatal(err)
	}
	return id
}

func getFTPHint(t *testing.T, h http.Handler) *store.FTPHint {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ftp-hint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, хочу 200", rec.Code)
	}
	var got *store.FTPHint
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("разбор ответа %q: %v", rec.Body.String(), err)
	}
	return got
}

// TestFTPHintThreshold — порог строгий: ровно +2% не подсказка, +2.1% — подсказка.
func TestFTPHintThreshold(t *testing.T) {
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)

	t.Run("ниже порога", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		addHintWorkout(t, db, false, start, 210)
		if got := getFTPHint(t, h); got != nil {
			t.Errorf("подсказка при оценке ниже FTP: %+v", got)
		}
	})

	t.Run("ровно +2%", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		// 0.95 × 214.75 = 204.0125 → оценка 204.0, ровно +2.0%
		addHintWorkout(t, db, false, start, 214.75)
		if got := getFTPHint(t, h); got != nil {
			t.Errorf("подсказка ровно на пороге: %+v", got)
		}
	})

	t.Run("+2.1%", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		id := addHintWorkout(t, db, false, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки при +2.1%")
		}
		if got.Kind != "outdoor" || got.Current != 200 || got.Estimate != 204.2 || got.DeltaPct != 2.1 {
			t.Errorf("подсказка = %+v", got)
		}
		if got.WorkoutID != id || got.Date.Format("2006-01-02") != "2026-10-31" {
			t.Errorf("тренировка в подсказке = id %d, дата %v", got.WorkoutID, got.Date)
		}
	})

	// Порог сравнивается по неокруглённой дельте: 2.01–2.04% раньше
	// округлялись до 2.0 и молча скрывались.
	for _, tt := range []struct {
		name    string
		current float64
		watts   float64
	}{
		{"+2.01%", 199, 213.6843},
		{"+2.04%", 196, 210.53},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, h := ftpHintServer(t, map[string]float64{"ftp": tt.current})
			addHintWorkout(t, db, false, start, tt.watts)
			got := getFTPHint(t, h)
			if got == nil {
				t.Fatalf("подсказка при %s скрыта", tt.name)
			}
			if got.DeltaPct != 2.0 {
				t.Errorf("%s: DeltaPct = %v, хочу подпись 2.0", tt.name, got.DeltaPct)
			}
		})
	}
}

// TestFTPHintKindFTP — станок сравнивается со станковым FTP, улица без
// ftp_outdoor — fallback на станковый.
func TestFTPHintKindFTP(t *testing.T) {
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)

	t.Run("станок и станковый FTP", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200, "ftp_outdoor": 100})
		id := addHintWorkout(t, db, true, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки для станка")
		}
		if got.Kind != "trainer" || got.Current != 200 || got.WorkoutID != id {
			t.Errorf("подсказка станка = %+v", got)
		}
	})

	t.Run("улица без ftp_outdoor", func(t *testing.T) {
		db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
		id := addHintWorkout(t, db, false, start, 214.95)
		got := getFTPHint(t, h)
		if got == nil {
			t.Fatal("нет подсказки с fallback на ftp")
		}
		if got.Kind != "outdoor" || got.Current != 200 || got.WorkoutID != id {
			t.Errorf("fallback улицы = %+v", got)
		}
	})
}

// TestFTPHintIgnoresFutureWorkout — будущая тренировка не даёт подсказку.
func TestFTPHintIgnoresFutureWorkout(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
	addHintWorkout(t, db, true, time.Now().Add(2*time.Hour), 300)
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка по будущей тренировке: %+v", got)
	}
}

// TestFTPHintNoWindow — без окна 1200 с подсказки нет.
func TestFTPHintNoWindow(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local), Duration: time.Hour,
	}, "hash-hint-no-1200", filepath.Join(t.TempDir(), "x.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, id, []store.PowerRecord{{WindowSec: 300, Watts: 400}}, 1); err != nil {
		t.Fatal(err)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка без окна 1200: %+v", got)
	}
}

// TestFTPHintPicksLargest — при превышении по обоим видам API отдаёт большее;
// станковый и уличный текущие FTP не путаются.
func TestFTPHintPicksLargest(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200, "ftp_outdoor": 100})
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)
	trainerID := addHintWorkout(t, db, true, start, 220)  // 209 Вт, +4.5%
	outdoorID := addHintWorkout(t, db, false, start, 214) // 203.3 Вт, +103.3%
	got := getFTPHint(t, h)
	if got == nil {
		t.Fatal("нет подсказки")
	}
	if got.Kind != "outdoor" || got.WorkoutID != outdoorID || got.Current != 100 {
		t.Errorf("выбрана не большая подсказка: %+v (станок id %d)", got, trainerID)
	}
}

func postProfileEntry(t *testing.T, h http.Handler, form url.Values) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// ftpUpdateTag — HTML-тег кнопки «Обновить FTP» из отрендеренной страницы.
func ftpUpdateTag(t *testing.T, body string) string {
	t.Helper()
	text := strings.Index(body, "Обновить FTP")
	if text < 0 {
		t.Fatalf("на странице нет кнопки «Обновить FTP»")
	}
	start := strings.LastIndex(body[:text], "<button")
	if start < 0 {
		t.Fatalf("нет открывающего тега кнопки «Обновить FTP»")
	}
	return body[start:text]
}

// TestFTPHintPagesAndButton — /progress и /workout показывают подсказку со
// ссылкой на тренировку, кнопка не передаёт дату тренировки, а её POST
// (kind/value, без date) убирает подсказку.
func TestFTPHintPagesAndButton(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp": 200})
	id := addHintWorkout(t, db, true, time.Now().Add(-2*time.Hour), 220)
	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки")
	}

	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: код %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	progress := get("/progress")
	if !strings.Contains(progress, fmt.Sprintf("/workout/%d", id)) {
		t.Errorf("/progress не ссылается на тренировку-источник %d", id)
	}
	detail := get(fmt.Sprintf("/workout/%d", id))
	if !strings.Contains(detail, "по этой тренировке") {
		t.Errorf("/workout не показывает подсказку по тренировке")
	}
	for name, body := range map[string]string{"/progress": progress, "/workout": detail} {
		tag := ftpUpdateTag(t, body)
		if !strings.Contains(tag, `hx-post="/profile/entry"`) {
			t.Errorf("%s: кнопка не отправляет профиль: %s", name, tag)
		}
		if strings.Contains(tag, "date") {
			t.Errorf("%s: кнопка передаёт дату тренировки: %s", name, tag)
		}
		if !strings.Contains(tag, fmt.Sprintf(`"workoutId":%d`, id)) {
			t.Errorf("%s: кнопка не передаёт тренировку-источник: %s", name, tag)
		}
	}

	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(hint.WorkoutID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST кнопки: код %d, хочу 200", code)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("после POST кнопки подсказка осталась: %+v", got)
	}
	if body := get("/progress"); strings.Contains(body, "Обновить FTP") {
		t.Error("/progress после POST всё ещё показывает кнопку обновления FTP")
	}
}

// TestFTPHintUpdateHidesHint — кнопка пишет запись на момент нажатия, а не на
// дату тренировки: после POST подсказка исчезает и для станка, и для улицы.
func TestFTPHintUpdateHidesHint(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour)
	cases := []struct {
		name        string
		kind        string
		profileKind string
		postKind    string
		trainer     bool
		current     float64
		watts       float64
	}{
		{"станок", "trainer", "ftp", "ftp", true, 200, 220},
		{"улица", "outdoor", "ftp_outdoor", "ftp_outdoor", false, 180, 210},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			db, h := ftpHintServer(t, map[string]float64{tt.profileKind: tt.current})
			addHintWorkout(t, db, tt.trainer, start, tt.watts)
			hint := getFTPHint(t, h)
			if hint == nil || hint.Kind != tt.kind {
				t.Fatalf("подсказка до POST = %+v, хочу вид %s", hint, tt.kind)
			}
			code := postProfileEntry(t, h, url.Values{
				"kind":      {tt.postKind},
				"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
				"workoutId": {strconv.FormatInt(hint.WorkoutID, 10)},
			})
			if code != http.StatusOK {
				t.Fatalf("POST: код %d, хочу 200", code)
			}
			if got := getFTPHint(t, h); got != nil {
				t.Errorf("подсказка %s не исчезла после POST: %+v", tt.kind, got)
			}
		})
	}
}

// TestFTPHintUpdateKeepsWorkoutsBeforeEntry — ручная запись без workoutId
// (старая форма /profile) действует с момента сохранения и не
// перештамповывает тренировки, начавшиеся до него (52 сохраняет 156/IF/TSS).
func TestFTPHintUpdateKeepsWorkoutsBeforeEntry(t *testing.T) {
	db, h := ftpTestServer(t)
	oldID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 28, 16, 59, 56, 0, time.UTC),
		Duration:  2064 * time.Second, Trainer: true,
		NormPower: 130, AvgPower: 106, HasPower: true,
	}, "hash-hint-52", filepath.Join(t.TempDir(), "52.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Date(2026, 10, 29, 17, 48, 57, 0, time.UTC),
		Duration:  3021 * time.Second, Trainer: true,
		NormPower: 97, AvgPower: 96, HasPower: true,
	}, "hash-hint-308", filepath.Join(t.TempDir(), "308.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 180}}, 1); err != nil {
		t.Fatal(err)
	}
	profileAt := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	if err := store.AddProfileEntry(db, "ftp", 156, profileAt); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	type snapshot struct {
		ftp  int
		kind string
		iff  float64
		tss  float64
		ver  int
	}
	snap := func(id int64) snapshot {
		t.Helper()
		w, err := store.GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot{w.FTP, w.FTPKind, w.IF, w.TSS, w.MetricsVersion}
	}
	beforeOld, beforeSource := snap(oldID), snap(sourceID)

	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки по 308")
	}
	if hint.Kind != "trainer" || hint.Current != 156 || hint.Estimate != 171 ||
		hint.DeltaPct != 9.6 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу trainer/156/171/+9.6/id %d", hint, sourceID)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp"}, "value": {"171"},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла после POST: %+v", got)
	}
	if got := snap(oldID); got != beforeOld {
		t.Errorf("52 до записи изменилась: было %+v, стало %+v", beforeOld, got)
	}
	if got := snap(sourceID); got != beforeSource {
		t.Errorf("308 до записи изменилась: было %+v, стало %+v", beforeSource, got)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("записей профиля %d, хочу 2", len(history))
	}
	if history[0].Kind != "ftp" || history[0].Value != 171 {
		t.Errorf("новая запись = %+v, хочу ftp 171", history[0])
	}
	if d := time.Since(history[0].RecordedAt); d < -time.Minute || d > time.Minute {
		t.Errorf("новая запись датирована %v, хочу момент нажатия", history[0].RecordedAt)
	}
	if !history[1].RecordedAt.Equal(profileAt) || history[1].Value != 156 {
		t.Errorf("старая запись = %+v, хочу ftp 156 на %v", history[1], profileAt)
	}
}

// TestFirstFTPOutdoorKeepsPastWorkouts — первая ftp_outdoor, добавленная
// сейчас, действует только на тренировки после записи: прошлые уличные
// сохраняют станковый FTP, IF/TSS; подсказка исчезает, а новая уличная
// тренировка получает новый FTP.
func TestFirstFTPOutdoorKeepsPastWorkouts(t *testing.T) {
	db, h := ftpTestServer(t)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-24 * time.Hour)
	var ids []int64
	for i := 0; i < 3; i++ {
		id, err := store.InsertWorkout(db, &store.Workout{
			StartTime: start.Add(time.Duration(i) * time.Hour),
			Duration:  time.Hour,
			NormPower: 140,
			AvgPower:  130,
			HasPower:  true,
		}, fmt.Sprintf("hash-first-outdoor-%d", i), filepath.Join(t.TempDir(), fmt.Sprintf("%d.fit", i)), 1)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	sourceID := ids[0]
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	type snapshot struct {
		ftp  int
		kind string
		iff  float64
		tss  float64
		ver  int
	}
	snap := func(id int64) snapshot {
		t.Helper()
		w, err := store.GetWorkout(db, id)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot{w.FTP, w.FTPKind, w.IF, w.TSS, w.MetricsVersion}
	}
	before := make([]snapshot, len(ids))
	for i, id := range ids {
		before[i] = snap(id)
		if before[i].ftp != 156 || before[i].kind != "trainer" {
			t.Fatalf("id=%d до записи: FTP=%d kind=%q, хочу 156/trainer", id, before[i].ftp, before[i].kind)
		}
	}

	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки до первой записи ftp_outdoor")
	}
	if hint.Kind != "outdoor" || hint.Current != 156 || hint.Estimate != 209 ||
		hint.DeltaPct != 34 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу outdoor/156/209/+34/id %d", hint, sourceID)
	}

	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp_outdoor"}, "value": {"209"},
	}); code != http.StatusOK {
		t.Fatalf("POST ftp_outdoor: код %d, хочу 200", code)
	}
	for i, id := range ids {
		if got := snap(id); got != before[i] {
			t.Errorf("id=%d перештампована задним числом: было %+v, стало %+v", id, before[i], got)
		}
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла после первой ftp_outdoor: %+v", got)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "209 Вт") {
		t.Errorf("/profile: код %d, нет текущего уличного FTP 209 Вт", rec.Code)
	}

	newID, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(time.Hour),
		Duration:  time.Hour,
		NormPower: 140,
		AvgPower:  130,
		HasPower:  true,
	}, "hash-first-outdoor-new", filepath.Join(t.TempDir(), "new.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	w, err := store.GetWorkout(db, newID)
	if err != nil {
		t.Fatal(err)
	}
	if w.FTP != 209 || w.FTPKind != "outdoor" {
		t.Errorf("новая уличная после записи: FTP=%d kind=%q, хочу 209/outdoor", w.FTP, w.FTPKind)
	}
}

// TestFTPHintOutdoorPageAndPost — уличная подсказка рендерится на деталке,
// кнопка отправляет kind=ftp_outdoor, после POST подсказка исчезает.
func TestFTPHintOutdoorPageAndPost(t *testing.T) {
	db, h := ftpHintServer(t, map[string]float64{"ftp_outdoor": 180})
	id := addHintWorkout(t, db, false, time.Now().Add(-2*time.Hour), 210)
	hint := getFTPHint(t, h)
	if hint == nil || hint.Kind != "outdoor" || hint.Current != 180 || hint.Estimate != 199.5 {
		t.Fatalf("подсказка = %+v, хочу outdoor/180/199.5", hint)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workout/%d: код %d", id, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, fmt.Sprintf("/workout/%d", id)) || !strings.Contains(body, "Обновить FTP") {
		t.Error("деталка не показывает уличную подсказку с кнопкой обновления")
	}
	tag := ftpUpdateTag(t, body)
	for _, want := range []string{`hx-post="/profile/entry"`, "ftp_outdoor", "199.5",
		fmt.Sprintf(`"workoutId":%d`, id)} {
		if !strings.Contains(tag, want) {
			t.Errorf("кнопка уличной подсказки не содержит %q: %s", want, tag)
		}
	}
	if strings.Contains(tag, "date") {
		t.Errorf("кнопка передаёт дату тренировки: %s", tag)
	}

	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp_outdoor"}, "value": {"199.5"}, "workoutId": {strconv.FormatInt(id, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST ftp_outdoor: код %d, хочу 200", code)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла после POST ftp_outdoor: %+v", got)
	}
}

// ftpSnapshot — состояние полей, которые меняет обновление FTP.
type ftpSnapshot struct {
	ftp  int
	kind string
	iff  float64
	tss  float64
	ver  int
}

func snapshotFTP(t *testing.T, db *sql.DB, id int64) ftpSnapshot {
	t.Helper()
	w, err := store.GetWorkout(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return ftpSnapshot{w.FTP, w.FTPKind, w.IF, w.TSS, w.MetricsVersion}
}

func insertFTPWorkout(t *testing.T, db *sql.DB, hash string, start time.Time, trainer bool,
	dur time.Duration, np, avg int) int64 {
	t.Helper()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: start, Duration: dur, Trainer: trainer,
		NormPower: np, AvgPower: avg, HasPower: true,
	}, hash, filepath.Join(t.TempDir(), hash+".fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestFTPHintUpdateOutdoorUsesSourceDate — кнопка обновляет только уличный FTP,
// запись действует с начала тренировки-источника: источник и последующие
// уличные пересчитаны, прошлые уличные и станковые — нет, подсказка исчезла.
func TestFTPHintUpdateOutdoorUsesSourceDate(t *testing.T) {
	db, h := ftpTestServer(t)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	olderID := insertFTPWorkout(t, db, "hash-upd-out-old", time.Now().AddDate(0, 0, -1), false, time.Hour, 140, 123)
	trainerID := insertFTPWorkout(t, db, "hash-upd-out-tr", time.Now().Add(-5*time.Hour), true, time.Hour, 96, 95)
	sourceID := insertFTPWorkout(t, db, "hash-upd-out-src", time.Now().Add(-2*time.Hour), false, 4292*time.Second, 162, 146)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 175.8}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	beforeOlder, beforeTrainer := snapshotFTP(t, db, olderID), snapshotFTP(t, db, trainerID)
	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет уличной подсказки")
	}
	if hint.Kind != "outdoor" || hint.Current != 156 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу outdoor/156/id %d", hint, sourceID)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp_outdoor"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}

	source, err := store.GetWorkout(db, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.FTP != int(hint.Estimate) || source.FTPKind != "outdoor" {
		t.Errorf("источник: FTP=%d kind=%q, хочу %v/outdoor", source.FTP, source.FTPKind, hint.Estimate)
	}
	wantIF := 162 / hint.Estimate
	if math.Abs(source.IF-wantIF) > 1e-9 {
		t.Errorf("IF источника = %v, хочу %v", source.IF, wantIF)
	}
	wantTSS := 4292 * 162 * wantIF / (hint.Estimate * 3600) * 100
	if math.Abs(source.TSS-wantTSS) > 1e-6 {
		t.Errorf("TSS источника = %v, хочу %v", source.TSS, wantTSS)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("прошлая уличная изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	if got := snapshotFTP(t, db, trainerID); got != beforeTrainer {
		t.Errorf("станковая изменилась: было %+v, стало %+v", beforeTrainer, got)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла: %+v", got)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var outdoor []store.ProfileEntry
	for _, e := range history {
		if e.Kind == "ftp_outdoor" {
			outdoor = append(outdoor, e)
		}
		if e.Synthetic {
			t.Errorf("уличное обновление создало служебную границу: %+v", e)
		}
	}
	if len(outdoor) != 1 || outdoor[0].Value != hint.Estimate ||
		!outdoor[0].RecordedAt.Equal(source.StartTime) {
		t.Errorf("запись ftp_outdoor = %+v, хочу %v на %v", outdoor, hint.Estimate, source.StartTime)
	}
}

// TestFTPHintUpdateTrainerUsesSourceDate — станковая кнопка пишет только `ftp`,
// запись действует с начала источника; более старые станковые не меняются.
func TestFTPHintUpdateTrainerUsesSourceDate(t *testing.T) {
	db, h := ftpTestServer(t)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	olderID := insertFTPWorkout(t, db, "hash-upd-tr-old", time.Now().AddDate(0, 0, -2), true, time.Hour, 120, 110)
	outdoorID := insertFTPWorkout(t, db, "hash-upd-tr-out", time.Now().AddDate(0, 0, -1), false, time.Hour, 140, 123)
	sourceID := insertFTPWorkout(t, db, "hash-upd-tr-src", time.Now().Add(-2*time.Hour), true, time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	beforeOlder, beforeOutdoor := snapshotFTP(t, db, olderID), snapshotFTP(t, db, outdoorID)
	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет станковой подсказки")
	}
	if hint.Kind != "trainer" || hint.Current != 156 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу trainer/156/id %d", hint, sourceID)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}

	source, err := store.GetWorkout(db, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if source.FTP != int(hint.Estimate) || source.FTPKind != "trainer" {
		t.Errorf("источник: FTP=%d kind=%q, хочу %v/trainer", source.FTP, source.FTPKind, hint.Estimate)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("старая станковая изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	if got := snapshotFTP(t, db, outdoorID); got != beforeOutdoor {
		t.Errorf("уличная до источника изменилась: было %+v, стало %+v", beforeOutdoor, got)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла: %+v", got)
	}
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		if e.Kind == "ftp_outdoor" {
			t.Errorf("станковая кнопка создала запись ftp_outdoor: %+v", e)
		}
	}
	if history[0].Kind != "ftp" || history[0].Value != hint.Estimate ||
		!history[0].RecordedAt.Equal(source.StartTime) {
		t.Errorf("новая запись = %+v, хочу ftp %v на %v", history[0], hint.Estimate, source.StartTime)
	}
}

// TestFTPHintUpdateTrainerKeepsBeforeFirstEntry — новая запись оказалась самой
// ранней: fallback на самую раннюю не должен перештамповать более старыв
// станковые. Старое значение держит запись-граница за секунду до источника,
// в том числе после повторного BackfillProfileStamps на старте.
func TestFTPHintUpdateTrainerKeepsBeforeFirstEntry(t *testing.T) {
	db, h := ftpTestServer(t)
	// Запись в будущем: сейчас действует 156 через fallback, но до источника
	// записей нет — новая запись стала бы самой ранней.
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	olderID := insertFTPWorkout(t, db, "hash-upd-tr-edge", time.Now().AddDate(0, 0, -1), true, time.Hour, 120, 110)
	source := time.Now().Add(-2 * time.Hour)
	sourceID := insertFTPWorkout(t, db, "hash-upd-tr-edge-src", source, true, time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	beforeOlder := snapshotFTP(t, db, olderID)
	hint := getFTPHint(t, h)
	if hint == nil || hint.Kind != "trainer" || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v", hint)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("старая станковая изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	// Повторная перештамповка при старте сервера не должна откатить прошлое.
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("после BackfillProfileStamps старая станковая изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	w, err := store.GetWorkout(db, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if w.FTP != int(hint.Estimate) || w.FTPKind != "trainer" {
		t.Errorf("источник: FTP=%d kind=%q, хочу %v/trainer", w.FTP, w.FTPKind, hint.Estimate)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла: %+v", got)
	}
}

// TestFTPHintUpdateTrainerExactAtSourceKeepsPast — запись профиля ровно в
// start_time источника не должна отменять границу: новая запись выигрывает
// tie-break по id, и без границы fallback применил бы её к прошлым тренировкам.
func TestFTPHintUpdateTrainerExactAtSourceKeepsPast(t *testing.T) {
	db, h := ftpTestServer(t)
	source := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	olderID := insertFTPWorkout(t, db, "hash-exact-old", source.AddDate(0, 0, -1), true,
		time.Hour, 120, 110)
	sourceID := insertFTPWorkout(t, db, "hash-exact-src", source, true, time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp", 156, source); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	beforeOlder := snapshotFTP(t, db, olderID)
	hint := getFTPHint(t, h)
	if hint == nil || hint.Kind != "trainer" || hint.Current != 156 ||
		hint.Estimate != 209 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу trainer/156/209/id %d", hint, sourceID)
	}
	form := url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}
	for i := 0; i < 2; i++ {
		if code := postProfileEntry(t, h, form); code != http.StatusOK {
			t.Fatalf("POST %d: код %d, хочу 200", i+1, code)
		}
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("старая станковая изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("после BackfillProfileStamps старая станковая изменилась: было %+v, стало %+v",
			beforeOlder, got)
	}
	w, err := store.GetWorkout(db, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if w.FTP != int(hint.Estimate) || w.FTPKind != "trainer" {
		t.Errorf("источник: FTP=%d kind=%q, хочу %v/trainer", w.FTP, w.FTPKind, hint.Estimate)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var synthetic []store.ProfileEntry
	var changed int
	for _, e := range history {
		if e.Synthetic {
			synthetic = append(synthetic, e)
		}
		if e.Kind == "ftp" && e.Value == hint.Estimate && !e.Synthetic {
			changed++
		}
	}
	if len(synthetic) != 1 {
		t.Fatalf("служебных границ %d, хочу 1: %+v", len(synthetic), history)
	}
	if synthetic[0].Value != 156 || !synthetic[0].RecordedAt.Equal(source.Add(-time.Second)) {
		t.Errorf("граница = %+v, хочу 156 на %v", synthetic[0], source.Add(-time.Second))
	}
	if changed != 1 {
		t.Errorf("записей ftp %v — %d, хочу 1: %+v", hint.Estimate, changed, history)
	}
}

// TestFTPHintUpdateTrainerKeepsOlderOutdoorFallback — старая уличная без
// ftp_outdoor живёт на fallback к станковому FTP. Даже если до источника
// станковых тренировок нет, новая запись не должна её перештамповать.
func TestFTPHintUpdateTrainerKeepsOlderOutdoorFallback(t *testing.T) {
	db, h := ftpTestServer(t)
	now := time.Now().Truncate(time.Second)
	olderID := insertFTPWorkout(t, db, "hash-fallback-old", now.AddDate(0, 0, -1), false,
		time.Hour, 140, 123)
	sourceID := insertFTPWorkout(t, db, "hash-fallback-src", now.Add(-2*time.Hour), true,
		time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	// Запись FTP появилась позже источника: новая станковая запись стала бы
	// самой ранней, а старая уличная получала бы её через fallback.
	if err := store.AddProfileEntry(db, "ftp", 200, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}

	beforeOlder := snapshotFTP(t, db, olderID)
	if beforeOlder.ftp != 200 || beforeOlder.kind != "trainer" {
		t.Fatalf("старая уличная до POST: %+v, хочу 200/trainer (fallback)", beforeOlder)
	}
	hint := getFTPHint(t, h)
	if hint == nil || hint.Kind != "trainer" || hint.Current != 200 ||
		hint.Estimate != 209 || hint.WorkoutID != sourceID {
		t.Fatalf("подсказка = %+v, хочу trainer/200/209/id %d", hint, sourceID)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("старая уличная на fallback изменилась: было %+v, стало %+v", beforeOlder, got)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFTP(t, db, olderID); got != beforeOlder {
		t.Errorf("после BackfillProfileStamps старая уличная изменилась: было %+v, стало %+v",
			beforeOlder, got)
	}
	w, err := store.GetWorkout(db, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if w.FTP != int(hint.Estimate) || w.FTPKind != "trainer" {
		t.Errorf("источник: FTP=%d kind=%q, хочу %v/trainer", w.FTP, w.FTPKind, hint.Estimate)
	}

	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var synthetic []store.ProfileEntry
	for _, e := range history {
		if e.Kind == "ftp_outdoor" {
			t.Errorf("станковая кнопка создала запись ftp_outdoor: %+v", e)
		}
		if e.Synthetic {
			synthetic = append(synthetic, e)
		}
	}
	if len(synthetic) != 1 || synthetic[0].Value != 200 ||
		!synthetic[0].RecordedAt.Equal(now.Add(-2*time.Hour-time.Second)) {
		t.Errorf("граница = %+v, хочу 200 за секунду до источника", synthetic)
	}
}

// TestSyntheticProfileBoundaryProtected — служебная граница помечена в
// истории и не удаляется ни кнопкой, ни через API.
func TestSyntheticProfileBoundaryProtected(t *testing.T) {
	db, h := ftpTestServer(t)
	source := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	sourceID := insertFTPWorkout(t, db, "hash-synth-src", source, true, time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.AddProfileEntry(db, "ftp", 156, source); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки")
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}); code != http.StatusOK {
		t.Fatalf("POST: код %d, хочу 200", code)
	}

	boundary := findSyntheticProfileEntry(t, db)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profile", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /profile: код %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "системная граница (не удалять)") {
		t.Error("/profile не помечает служебную границу")
	}
	if strings.Contains(body, fmt.Sprintf("/profile/entry/%d/delete", boundary.ID)) {
		t.Errorf("/profile показывает кнопку удаления служебной границы id=%d", boundary.ID)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/profile/entry/%d/delete", boundary.ID), nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("удаление границы: код %d, хочу 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "нельзя удалить") {
		t.Errorf("тело ошибки: %q", rec.Body.String())
	}
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var stillThere bool
	for _, e := range history {
		if e.ID == boundary.ID {
			stillThere = true
		}
	}
	if !stillThere {
		t.Error("служебная граница удалена через API")
	}
}

// findSyntheticProfileEntry — единственная служебная запись в истории.
func findSyntheticProfileEntry(t *testing.T, db *sql.DB) store.ProfileEntry {
	t.Helper()
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range history {
		if e.Synthetic {
			return e
		}
	}
	t.Fatalf("в истории нет служебной границы: %+v", history)
	return store.ProfileEntry{}
}

// TestProfileEntryWorkoutIDOnlyForFTP — workoutId имеет смысл только для
// станкового/уличного FTP: weight с workoutId не должен молча превращаться
// в запись ftp на дату тренировки. date вместе с workoutId тоже отклоняется.
func TestProfileEntryWorkoutIDOnlyForFTP(t *testing.T) {
	db, h := ftpTestServer(t)
	trainerID := insertFTPWorkout(t, db, "hash-wid-tr", time.Now().Add(-2*time.Hour), true,
		time.Hour, 140, 130)
	outdoorID := insertFTPWorkout(t, db, "hash-wid-out", time.Now().Add(-time.Hour), false,
		time.Hour, 140, 130)
	for _, id := range []int64{trainerID, outdoorID} {
		if code := postProfileEntry(t, h, url.Values{
			"kind":      {"weight"},
			"value":     {"75"},
			"workoutId": {strconv.FormatInt(id, 10)},
		}); code != http.StatusBadRequest {
			t.Errorf("weight с workoutId=%d: код %d, хочу 400", id, code)
		}
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind":      {"ftp"},
		"value":     {"200"},
		"workoutId": {strconv.FormatInt(trainerID, 10)},
		"date":      {"2026-10-30"},
	}); code != http.StatusBadRequest {
		t.Errorf("date вместе с workoutId: код %d, хочу 400", code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("отклонённые формы создали %d записей профиля", n)
	}

	// Правильные виды с workoutId работают как раньше.
	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp"}, "value": {"200"}, "workoutId": {strconv.FormatInt(trainerID, 10)},
	}); code != http.StatusOK {
		t.Errorf("ftp с workoutId: код %d, хочу 200", code)
	}
	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp_outdoor"}, "value": {"190"}, "workoutId": {strconv.FormatInt(outdoorID, 10)},
	}); code != http.StatusOK {
		t.Errorf("ftp_outdoor с workoutId: код %d, хочу 200", code)
	}
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Kind != "ftp_outdoor" || history[1].Kind != "ftp" {
		t.Errorf("история после валидных форм = %+v, хочу ftp_outdoor и ftp", history)
	}
}

// TestFTPHintUpdateKindMismatch — подсказка и тренировка-источник разного вида
// (улица/станок) — 400, запись профиля не создаётся.
func TestFTPHintUpdateKindMismatch(t *testing.T) {
	db, h := ftpTestServer(t)
	trainerID := insertFTPWorkout(t, db, "hash-upd-mismatch-tr", time.Now().Add(-2*time.Hour), true, time.Hour, 140, 130)
	outdoorID := insertFTPWorkout(t, db, "hash-upd-mismatch-out", time.Now().Add(-2*time.Hour), false, time.Hour, 140, 130)
	for _, form := range []url.Values{
		{"kind": {"ftp_outdoor"}, "value": {"200"}, "workoutId": {strconv.FormatInt(trainerID, 10)}},
		{"kind": {"ftp"}, "value": {"200"}, "workoutId": {strconv.FormatInt(outdoorID, 10)}},
	} {
		if code := postProfileEntry(t, h, form); code != http.StatusBadRequest {
			t.Errorf("%v: код %d, хочу 400", form, code)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_history`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("создано %d записей профиля при ошибке, хочу 0", n)
	}
}

// TestFTPHintUpdateWorkoutNotFound — неизвестный workoutId — 404.
func TestFTPHintUpdateWorkoutNotFound(t *testing.T) {
	_, h := ftpTestServer(t)
	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp"}, "value": {"200"}, "workoutId": {"999999"},
	}); code != http.StatusNotFound {
		t.Errorf("код %d, хочу 404", code)
	}
}

// TestFTPHintUpdateTwiceIsIdempotent — повторное нажатие не создаёт дубль
// записи на ту же дату и значение.
func TestFTPHintUpdateTwiceIsIdempotent(t *testing.T) {
	db, h := ftpTestServer(t)
	if err := store.AddProfileEntry(db, "ftp", 156, time.Now().AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	sourceID := insertFTPWorkout(t, db, "hash-upd-twice", time.Now().Add(-2*time.Hour), true, time.Hour, 140, 130)
	if err := store.SetWorkoutMMP(db, sourceID, []store.PowerRecord{{WindowSec: 1200, Watts: 220}}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.BackfillProfileStamps(db); err != nil {
		t.Fatal(err)
	}
	if _, err := importer.BackfillMetrics(db); err != nil {
		t.Fatal(err)
	}
	hint := getFTPHint(t, h)
	if hint == nil {
		t.Fatal("нет подсказки")
	}
	form := url.Values{
		"kind":      {"ftp"},
		"value":     {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
		"workoutId": {strconv.FormatInt(sourceID, 10)},
	}
	for i := 0; i < 2; i++ {
		if code := postProfileEntry(t, h, form); code != http.StatusOK {
			t.Fatalf("POST %d: код %d, хочу 200", i+1, code)
		}
	}
	history, err := store.ProfileHistory(db)
	if err != nil {
		t.Fatal(err)
	}
	var same int
	for _, e := range history {
		if e.Kind == "ftp" && e.Value == hint.Estimate {
			same++
		}
	}
	if same != 1 {
		t.Errorf("записей ftp %v — %d, хочу 1: %+v", hint.Estimate, same, history)
	}
}
