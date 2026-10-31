package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// testFlagFixture — рабочая тренировка с ровной MMP-кривой и помеченный
// замер с завышенными короткими окнами: по умолчанию он не должен попадать
// ни в рекорды, ни в CP-фит.
func testFlagFixture(t *testing.T) (normal, test int64, h http.Handler) {
	t.Helper()
	d, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	now := time.Now()
	normal, err = store.InsertWorkout(d, &store.Workout{StartTime: now.Add(-2 * time.Hour), Duration: time.Hour},
		"hash-api-normal", filepath.Join(t.TempDir(), "n.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(d, normal, []store.PowerRecord{
		{WindowSec: 120, Watts: 200}, {WindowSec: 300, Watts: 180},
		{WindowSec: 600, Watts: 170}, {WindowSec: 1200, Watts: 160},
	}, 1); err != nil {
		t.Fatal(err)
	}
	test, err = store.InsertWorkout(d, &store.Workout{StartTime: now.Add(-time.Hour), Duration: time.Hour},
		"hash-api-test", filepath.Join(t.TempDir(), "t.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutMMP(d, test, []store.PowerRecord{
		{WindowSec: 120, Watts: 400}, {WindowSec: 300, Watts: 300}, {WindowSec: 600, Watts: 250},
	}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.SetWorkoutTest(d, test, true); err != nil {
		t.Fatal(err)
	}
	h, err = Handler(d, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return normal, test, h
}

func getJSON(t *testing.T, h http.Handler, path string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: код %d", path, rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// TestRecordsAPIIncludeTests — по умолчанию тест исключён, includeTests=1
// возвращает прежнюю картину; мусор в параметре — 400.
func TestRecordsAPIIncludeTests(t *testing.T) {
	normal, test, h := testFlagFixture(t)

	var recs []store.Record
	getJSON(t, h, "/api/records?period=all", &recs)
	if len(recs) == 0 {
		t.Fatal("без тестов пусто")
	}
	for _, r := range recs {
		if r.WorkoutID != normal {
			t.Errorf("окно %d: id=%d, хочу рабочую %d", r.WindowSec, r.WorkoutID, normal)
		}
	}

	recs = nil
	getJSON(t, h, "/api/records?period=all&includeTests=1", &recs)
	if len(recs) != 4 {
		t.Fatalf("%d окон с тестами, хочу 4", len(recs))
	}
	if recs[0].WindowSec != 120 || recs[0].Watts != 400 || recs[0].WorkoutID != test {
		t.Errorf("окно 120 с тестом: %+v, хочу 400 от %d", recs[0], test)
	}
	if recs[3].WindowSec != 1200 || recs[3].WorkoutID != normal {
		t.Errorf("окно 1200: %+v, хочу рабочую (у теста его нет)", recs[3])
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/records?includeTests=yes", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("includeTests=yes: код %d, хочу 400", rec.Code)
	}
}

// TestCPAPIIncludeTests — CP по умолчанию фитируется без замера, includeTests
// добавляет его окна.
func TestCPAPIIncludeTests(t *testing.T) {
	normal, test, h := testFlagFixture(t)

	var clean cpResult
	getJSON(t, h, "/api/cp?period=all", &clean)
	if clean.CP == nil || clean.Points != 4 {
		t.Fatalf("без тестов: %+v, хочу 4 точки", clean)
	}

	var withTests cpResult
	getJSON(t, h, "/api/cp?period=all&includeTests=1", &withTests)
	if withTests.CP == nil || withTests.Points != 4 {
		t.Fatalf("с тестами: %+v, хочу 4 точки", withTests)
	}
	if *clean.CP == *withTests.CP {
		t.Errorf("CP не изменился от теста: %v", *clean.CP)
	}
	// тест выигрывает 120/300/600 — в рекордах с includeTests он и должен быть
	var recs []store.Record
	getJSON(t, h, "/api/records?period=all&includeTests=1", &recs)
	if recs[0].WorkoutID != test || recs[0].WorkoutID == normal {
		t.Errorf("с тестами короткие окна не у теста: %+v", recs[0])
	}
}

// TestWorkoutPRsAPIIncludeTests — у тестовой тренировки PR-флагов по умолчанию
// нет; includeTests=1 их показывает.
func TestWorkoutPRsAPIIncludeTests(t *testing.T) {
	normal, test, h := testFlagFixture(t)

	var prs map[string][]string
	getJSON(t, h, fmt.Sprintf("/api/workout/%d/prs", test), &prs)
	if len(prs) != 0 {
		t.Errorf("PR теста по умолчанию: %v, хочу пусто", prs)
	}

	prs = nil
	getJSON(t, h, fmt.Sprintf("/api/workout/%d/prs?includeTests=1", test), &prs)
	if got := prs["120"]; len(got) == 0 || got[0] != "all" {
		t.Errorf("PR теста с includeTests: %v, хочу all", prs)
	}

	prs = nil
	getJSON(t, h, fmt.Sprintf("/api/workout/%d/prs", normal), &prs)
	if got := prs["120"]; len(got) != 4 || got[0] != "all" {
		t.Errorf("PR рабочей без тестов: %v, хочу все периоды", prs)
	}
}

// TestTestToggle — переключатель на деталке ставит и снимает флаг; неизвестный
// id — 404, страница показывает пометку.
func TestTestToggle(t *testing.T) {
	_, test, h := testFlagFixture(t)
	toggle := func(id int64, v string) int {
		t.Helper()
		body := url.Values{"value": {v}}.Encode()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/workout/%d/test", id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := toggle(test, "0"); code != http.StatusOK {
		t.Fatalf("снятие флага: код %d", code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", test), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("страница: код %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<span class=\"tag\">тест</span>") {
		t.Errorf("после снятия на странице остался тег «тест»")
	}
	if code := toggle(test, "1"); code != http.StatusOK {
		t.Fatalf("установка флага: код %d", code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/workout/%d", test), nil))
	if !strings.Contains(rec.Body.String(), "<span class=\"tag\">тест</span>") {
		t.Errorf("на странице нет тега «тест»")
	}
	if code := toggle(99999, "1"); code != http.StatusNotFound {
		t.Errorf("неизвестный id: код %d, хочу 404", code)
	}
}
