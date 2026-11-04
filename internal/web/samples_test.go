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
	get := func(max int) []store.Sample {
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
		return got
	}

	for _, max := range []int{1, 10, 100, 500, 5281, 6000} {
		got := get(max)
		if len(got) > max {
			t.Errorf("max=%d: вернулось %d точек — больше лимита", max, len(got))
		}
		if max <= n && len(got) < max/2 {
			t.Errorf("max=%d: вернулось %d точек — подозрительно мало", max, len(got))
		}
	}

	// при max >= total данные не теряются: все offset на месте
	all := get(n)
	if len(all) != n {
		t.Fatalf("max=%d: %d точек, хочу %d", n, len(all), n)
	}
	if all[0].Offset != 0 || all[n-1].Offset != int64(n-1) {
		t.Errorf("диапазон offset %d..%d, хочу 0..%d", all[0].Offset, all[n-1].Offset, n-1)
	}
	unique := make(map[int64]struct{}, n)
	for _, s := range all {
		unique[s.Offset] = struct{}{}
	}
	if len(unique) != n {
		t.Errorf("уникальных offset %d, хочу %d", len(unique), n)
	}

	// при прореживании offset растёт и покрывает весь диапазон тренировки
	bucketed := get(500)
	if len(bucketed) < 2 {
		t.Fatalf("max=500: %d точек", len(bucketed))
	}
	for i := 1; i < len(bucketed); i++ {
		if bucketed[i].Offset <= bucketed[i-1].Offset {
			t.Fatalf("offset не растёт: %d после %d", bucketed[i].Offset, bucketed[i-1].Offset)
		}
	}
	if bucketed[0].Offset > 20 || bucketed[len(bucketed)-1].Offset < int64(n-1-20) {
		t.Errorf("бакеты покрывают %d..%d, хочу почти весь диапазон 0..%d",
			bucketed[0].Offset, bucketed[len(bucketed)-1].Offset, n-1)
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
		{name: "raw и max", query: "raw=1&max=42", want: samplesQuery{Max: defaultSampleMax, Raw: true}},
		{name: "raw с мусорным max", query: "raw=1&max=abc", want: samplesQuery{Max: defaultSampleMax, Raw: true}},
		{name: "raw=0 и мусорный max", query: "raw=0&max=abc", wantErr: true},
		{name: "дробное", query: "max=500.5", wantErr: true},
		{name: "плюс в query — это пробел", query: "max=+5", wantErr: true},
		{name: "закодированный плюс", query: "max=%2B5", want: samplesQuery{Max: 5}},
		{name: "мусор", query: "max=abc", wantErr: true},
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
	if rec, raw := get(t, "?raw=1&max=abc"); rec.Code != http.StatusOK || len(raw) != n {
		t.Errorf("raw=1&max=abc: код %d, %d точек; хочу 200 и %d (max при raw игнорируется)",
			rec.Code, len(raw), n)
	}

	// неизвестная тренировка — 404
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/workout/99999/samples", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("неизвестный id: код %d, хочу 404", rec.Code)
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

// TestTooManySamples — потолок raw=1 проверяется без базы.
func TestTooManySamples(t *testing.T) {
	if err := tooManySamples(sampleMaxLimit); err != nil {
		t.Errorf("ровно на потолке: %v, хочу nil", err)
	}
	if err := tooManySamples(sampleMaxLimit + 1); err == nil {
		t.Error("выше потолка: ошибки нет")
	}
}

// TestSamplesRawNulls — raw=1 честно отдаёт дыры: NULL-поля приходят нулями,
// а Has*-флаги говорят, что данных не было.
func TestSamplesRawNulls(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{StartTime: time.Now(), Duration: 2 * time.Second},
		"hash-raw-nulls", filepath.Join(t.TempDir(), "test.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	samples := []store.Sample{
		{Offset: 0}, // все датчики отвалились
		{Offset: 1, Power: 150, HasPwr: true, HR: 130}, // только мощность и пульс
	}
	if err := store.InsertSamples(db, id, samples); err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/workout/%d/samples?raw=1", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	var got []store.Sample
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d точек, хочу 2", len(got))
	}
	if got[0].HasPwr || got[0].HasCad || got[0].HasPos || got[0].HasAlt || got[0].HasDist {
		t.Errorf("дыра превратилась в данные: %+v", got[0])
	}
	if !got[1].HasPwr || got[1].Power != 150 || got[1].HR != 130 {
		t.Errorf("вторая запись: %+v, хочу power=150 hr=130", got[1])
	}
}

// TestSamplesEmptyArray — у известной тренировки без сэмплов ответ [], а не null.
func TestSamplesEmptyArray(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id, err := store.InsertWorkout(db, &store.Workout{StartTime: time.Now(), Duration: time.Second},
		"hash-empty-array", filepath.Join(t.TempDir(), "test.fit"), 1)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Handler(db, NewSyncManager(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/workout/%d/samples", id), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("тело %q, хочу []", body)
	}
}
