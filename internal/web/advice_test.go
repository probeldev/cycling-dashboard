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

// TestWorkoutAdviceBlock — на деталке есть блок «Что говорят данные» и карточки
// по фактическим данным тренировки, не больше пяти.
func TestWorkoutAdviceBlock(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: time.Hour,
		AvgCadence: 66, AvgPower: 120, MaxPower: 300,
	}, "hash-advice", filepath.Join(t.TempDir(), "x.fit"), 1)
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
	for _, want := range []string{"Что говорят данные", "Низкий каденс", "Пульса нет", "Нет RPE"} {
		if !strings.Contains(body, want) {
			t.Errorf("на деталке нет %q", want)
		}
	}
	if n := strings.Count(body, `class="advice `); n > 5 {
		t.Errorf("карточек %d, хочу не больше 5", n)
	}
	if strings.Contains(body, "Рваная езда") || strings.Contains(body, "Пропуски данных") {
		t.Errorf("карточка без данных: %q", body)
	}
}
