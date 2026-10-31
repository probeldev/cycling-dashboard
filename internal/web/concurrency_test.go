package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestConcurrentProfileEntries — 8 параллельных POST /profile/entry не должны
// падать с SQLITE_BUSY: busy_timeout ждёт освобождения write-lock.
func TestConcurrentProfileEntries(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now().Add(-time.Hour), Duration: time.Hour,
		NormPower: 140, AvgPower: 123, FTP: 156,
	}, "hash-concurrent", filepath.Join(t.TempDir(), "x.fit"), 1); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf("kind=ftp&value=%d&date=2026-10-30", 100+i)
			req := httptest.NewRequest(http.MethodPost, "/profile/entry", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("запись %d: код %d", i, code)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM profile_history`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Errorf("в profile_history %d записей, хочу %d", count, n)
	}
}
