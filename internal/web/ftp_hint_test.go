package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
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
	at := time.Now().Add(-time.Hour)
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
	}

	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp"}, "value": {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
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
				"kind":  {tt.postKind},
				"value": {strconv.FormatFloat(hint.Estimate, 'f', 1, 64)},
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

// TestFTPHintUpdateKeepsWorkoutsBeforeEntry — сценарий 308: запись на now не
// перештамповывает тренировки, начавшиеся до её применения (52 сохраняет
// 156/IF/TSS), а подсказка +9.6% после нажатия кнопки исчезает.
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
	for _, want := range []string{`hx-post="/profile/entry"`, "ftp_outdoor", "199.5"} {
		if !strings.Contains(tag, want) {
			t.Errorf("кнопка уличной подсказки не содержит %q: %s", want, tag)
		}
	}
	if strings.Contains(tag, "date") {
		t.Errorf("кнопка передаёт дату тренировки: %s", tag)
	}

	if code := postProfileEntry(t, h, url.Values{
		"kind": {"ftp_outdoor"}, "value": {"199.5"},
	}); code != http.StatusOK {
		t.Fatalf("POST ftp_outdoor: код %d, хочу 200", code)
	}
	if got := getFTPHint(t, h); got != nil {
		t.Errorf("подсказка не исчезла после POST ftp_outdoor: %+v", got)
	}
}
