package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"cycling-dashboard/internal/store"
)

// TestSamplesMaxBound — ?max=N возвращает не больше N точек. Объём как
// у тренировки 418: 5281 сэмпл с шагом 1 с.
func TestSamplesMaxBound(t *testing.T) {
	const n = 5281
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: n * time.Second, AvgPower: 200, MaxPower: 400,
	}, "hash-max-bound", filepath.Join(t.TempDir(), "test.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]store.Sample, n)
	for i := range samples {
		samples[i] = store.Sample{Offset: int64(i), Power: 200, HasPwr: true}
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	count := func(max int) int {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/workout/%d/samples?max=%d", id, max), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("max=%d: код %d", max, rec.Code)
		}
		var got []store.Sample
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("max=%d: битый JSON: %v", max, err)
		}
		return len(got)
	}

	for _, max := range []int{1, 10, 100, 500, 5281, 6000} {
		got := count(max)
		if got > max {
			t.Errorf("max=%d: вернулось %d точек — больше лимита", max, got)
		}
		if max <= n && got < max/2 {
			t.Errorf("max=%d: вернулось %d точек — подозрительно мало", max, got)
		}
	}
	if got := count(n); got < n-1 {
		t.Errorf("max=%d: вернулось %d точек, хочу почти все исходные", n, got)
	}
}

func TestParseSamplesQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    samplesQuery
		wantErr bool
	}{
		{name: "дефолт", query: "", want: samplesQuery{Max: defaultSampleMax}},
		{name: "явный max", query: "max=5000", want: samplesQuery{Max: 5000}},
		{name: "max=1", query: "max=1", want: samplesQuery{Max: 1}},
		{name: "потолок", query: fmt.Sprintf("max=%d", sampleMaxLimit), want: samplesQuery{Max: sampleMaxLimit}},
		{name: "пустое значение — дефолт", query: "max=", want: samplesQuery{Max: defaultSampleMax}},
		{name: "raw", query: "raw=1", want: samplesQuery{Max: defaultSampleMax, Raw: true}},
		{name: "raw выключен", query: "raw=0", want: samplesQuery{Max: defaultSampleMax}},
		{name: "raw и max", query: "raw=1&max=42", want: samplesQuery{Max: 42, Raw: true}},
		{name: "мусор", query: "max=abc", wantErr: true},
		{name: "дробное", query: "max=1.5", wantErr: true},
		{name: "ноль", query: "max=0", wantErr: true},
		{name: "отрицательное", query: "max=-5", wantErr: true},
		{name: "выше потолка", query: fmt.Sprintf("max=%d", sampleMaxLimit+1), wantErr: true},
		{name: "мусор в raw", query: "raw=maybe", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := url.ParseQuery(tt.query)
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseSamplesQuery(q)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("%q: жду ошибку, получил %+v", tt.query, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: неожиданная ошибка: %v", tt.query, err)
			}
			if got != tt.want {
				t.Errorf("%q: %+v, хочу %+v", tt.query, got, tt.want)
			}
		})
	}
}

// TestSamplesHandler — end-to-end на временной базе: 400 на мусор,
// raw отдаёт все сэмплы с честными пиками, max усредняет.
func TestSamplesHandler(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const n = 100
	id, err := store.InsertWorkout(db, &store.Workout{
		StartTime: time.Now(), Duration: n * time.Second, AvgPower: 100, MaxPower: 900,
	}, "hash-samples-test", filepath.Join(t.TempDir(), "test.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]store.Sample, 0, n)
	for i := 0; i < n; i++ {
		p := 100
		if i == n/2 {
			p = 900 // короткий пик — первым делом теряется при усреднении
		}
		samples = append(samples, store.Sample{Offset: int64(i), Power: p, HasPwr: true})
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}

	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(t *testing.T, query string) (*httptest.ResponseRecorder, []store.Sample) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/workout/%d/samples%s", id, query), nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec, nil
		}
		var out []store.Sample
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: битый JSON: %v", query, err)
		}
		return rec, out
	}
	peak := func(list []store.Sample) int {
		m := 0
		for _, s := range list {
			m = max(m, s.Power)
		}
		return m
	}

	if rec, _ := get(t, "?max=abc"); rec.Code != http.StatusBadRequest {
		t.Errorf("max=abc: код %d, хочу 400 (тело: %q)", rec.Code, rec.Body.String())
	}
	if rec, _ := get(t, "?max=-1"); rec.Code != http.StatusBadRequest {
		t.Errorf("max=-1: код %d, хочу 400", rec.Code)
	}

	if _, raw := get(t, "?raw=1"); len(raw) != n || peak(raw) != 900 {
		t.Errorf("raw=1: %d точек, пик %d Вт; хочу %d точек и пик 900", len(raw), peak(raw), n)
	}
	if _, def := get(t, ""); len(def) != n || peak(def) != 900 {
		t.Errorf("без параметров: %d точек, пик %d Вт; хочу %d и 900 (дефолт 500 > числа сэмплов)",
			len(def), peak(def), n)
	}
	// max=10: бакеты по ~11 с, пик размазывается
	if _, avgd := get(t, "?max=10"); len(avgd) >= n || peak(avgd) >= 900 {
		t.Errorf("max=10: %d точек, пик %d Вт; хочу меньше %d точек и пик ниже 900",
			len(avgd), peak(avgd), n)
	}
}
