package web

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/importer"
	"cycling-dashboard/internal/store"
)

//go:embed templates static
var assets embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"km":        func(v float64) string { return trimFloat(v) },
	"hours":     func(h float64) string { return trimFloat(h) },
	"sec2h":     func(sec int64) string { return trimFloat(float64(sec) / 3600) },
	"secs":      func(s int) time.Duration { return time.Duration(s) * time.Second },
	"date":      func(t time.Time) string { return t.Format("02.01.2006") },
	"trimFloat": trimFloat,
	"divf":      func(a int, b float64) float64 { return float64(a) / b },
	"pizzas": func(kcal int) string {
		p := float64(kcal) / 2400 // Domino's Пепперони Делюкс 100, L (950 г × 252.22 ккал/100 г) ≈ 2400 ккал
		if p < 0.3 {
			return ""
		}
		return strconv.FormatFloat(p, 'f', 1, 64) + " пицц пепперони"
	},
	"datetime": func(t time.Time) string { return t.Format("02.01.2006 15:04") },
	"dur": func(d time.Duration) string {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		s := int(d.Seconds()) % 60
		if h > 0 {
			return strconv.Itoa(h) + " ч " + strconv.Itoa(m) + " мин"
		}
		if m > 0 {
			return strconv.Itoa(m) + " мин " + strconv.Itoa(s) + " с"
		}
		return strconv.Itoa(s) + " с"
	},
	"monthName": func(m time.Month) string {
		names := [...]string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
			"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}
		return names[m-1]
	},
}).ParseFS(assets, "templates/*.html"))

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func Handler(db *sql.DB, sync *SyncManager, syncDir string, afterSync func()) (http.Handler, error) {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServerFS(static)))
	sync.routes(r, syncDir, afterSync)
	profileRoutes(r, db)
	progressRoutes(r, db)

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		years, err := store.ListTree(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, "index.html", map[string]any{"Years": years})
	})

	r.Get("/workout/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		wo, err := store.GetWorkout(db, id)
		if err != nil {
			notFoundOr500(w, req, err)
			return
		}
		mmp, err := store.WorkoutMMP(db, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		prs, err := store.WorkoutPRs(db, id, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bikes, err := store.Bikes(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, "workout.html", map[string]any{
			"W": wo, "MMP": mmpCards(mmp, prs), "Bikes": bikes, "RPEOptions": rpeOptions,
		})
	})

	r.Post("/workout/{id}/trainer", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if err := store.SetTrainerOverrideTx(tx, id, req.FormValue("value") == "1"); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.NotFound(w, req)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// FTP станка и улицы — разные: после смены типа метрики надо пересчитать
		// сразу, иначе до рестарта API отдаст IF/TSS от прежней базы
		if _, err := importer.BackfillMetricsTx(tx); err != nil {
			http.Error(w, "не удалось пересчитать метрики: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(id, 10))
	})

	r.Post("/workout/{id}/context", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if _, err := store.GetWorkout(db, id); err != nil {
			http.NotFound(w, req)
			return
		}
		rpe := 0
		if raw := req.FormValue("rpe"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > 10 {
				http.Error(w, "RPE: нужно целое число от 1 до 10", http.StatusBadRequest)
				return
			}
			rpe = v
		}
		notes := strings.TrimSpace(req.FormValue("notes"))
		if len([]rune(notes)) > maxNotesLen {
			http.Error(w, "заметка длиннее 2000 символов", http.StatusBadRequest)
			return
		}
		if err := store.SetWorkoutContext(db, id, rpe, notes); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(id, 10))
	})

	r.Post("/workout/{id}/bike", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if _, err := store.GetWorkout(db, id); err != nil {
			notFoundOr500(w, req, err)
			return
		}
		var bikeID *int64
		if raw := req.FormValue("bike_id"); raw != "" {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				http.Error(w, "bike_id: нужно целое число", http.StatusBadRequest)
				return
			}
			if _, err := store.BikeByID(db, v); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					http.Error(w, "неизвестный велосипед", http.StatusBadRequest)
					return
				}
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			bikeID = &v
		}
		if err := store.SetWorkoutBike(db, id, bikeID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(id, 10))
	})

	r.Post("/workout/{id}/name", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err := store.SetName(db, id, req.FormValue("name")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(id, 10))
	})

	r.Get("/api/workouts", func(w http.ResponseWriter, req *http.Request) {
		list, err := store.ListWorkouts(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	})

	r.Get("/api/workouts/latest", func(w http.ResponseWriter, req *http.Request) {
		wo, err := store.LatestWorkout(db)
		if err != nil {
			notFoundOr500(w, req, err)
			return
		}
		writeJSON(w, wo)
	})

	r.Get("/api/workout/{id}", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		wo, err := store.GetWorkout(db, id)
		if err != nil {
			notFoundOr500(w, req, err)
			return
		}
		writeJSON(w, wo)
	})

	r.Get("/api/workout/{id}/samples", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		q, err := parseSamplesQuery(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// как и /dist: неизвестная тренировка — 404, а не 200 null
		if _, err := store.GetWorkout(db, id); err != nil {
			notFoundOr500(w, req, err)
			return
		}
		var samples []store.Sample
		if q.Raw {
			total, err := store.CountSamples(db, id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := tooManySamples(total); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			samples, err = store.RawSamples(db, id)
		} else {
			samples, err = store.Samples(db, id, q.Max)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, samples)
	})

	r.Get("/api/timeline", func(w http.ResponseWriter, req *http.Request) {
		from, to, err := parseTimelineRange(req.URL.Query(), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		days, err := store.Timeline(db, from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, days)
	})

	r.Get("/api/records", func(w http.ResponseWriter, req *http.Request) {
		from, to, prevDays, err := parseRecordsPeriod(req.URL.Query(), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err := store.BestRecords(db, from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if prevDays > 0 {
			prev, err := store.BestRecords(db, from.AddDate(0, 0, -prevDays), from)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			byWindow := make(map[int]store.Record, len(prev))
			for _, r := range prev {
				byWindow[r.WindowSec] = r
			}
			for i := range records {
				if p, ok := byWindow[records[i].WindowSec]; ok {
					records[i].Previous = &p
				}
			}
		}
		writeJSON(w, records)
	})

	r.Get("/api/workout/{id}/prs", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if _, err := store.GetWorkout(db, id); err != nil {
			notFoundOr500(w, req, err)
			return
		}
		prs, err := store.WorkoutPRs(db, id, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, prs)
	})

	r.Get("/api/cp", func(w http.ResponseWriter, req *http.Request) {
		from, to, _, err := parseRecordsPeriod(req.URL.Query(), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err := store.BestRecords(db, from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, fitCP(powerPoints(records)))
	})

	r.Get("/api/workout/{id}/mmp", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if _, err := store.GetWorkout(db, id); err != nil {
			notFoundOr500(w, req, err)
			return
		}
		records, err := store.WorkoutMMP(db, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, records)
	})

	r.Get("/api/weeks", func(w http.ResponseWriter, req *http.Request) {
		n, err := parseWeeksN(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		weeks, err := store.Weeks(db, n, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, weeks)
	})

	r.Get("/api/workout/{id}/dist", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		wo, err := store.GetWorkout(db, id)
		if err != nil {
			notFoundOr500(w, req, err)
			return
		}
		// распределение считаем по сырым сэмплам: бакет-усреднение смешало бы
		// нули мощности с педалированием на записи 2 Гц и убило бы накат
		samples, err := store.RawSamples(db, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, distributions(samples, wo.FTP))
	})

	return r, nil
}

// notFoundOr500 — 404 только для отсутствующей строки, остальное — 500:
// иначе ошибка базы маскируется под «не найдено».
func notFoundOr500(w http.ResponseWriter, req *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, req)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// render рендерит шаблон в буфер и отдаёт его только при успехе: иначе ошибка
// на середине шаблона оставляла бы клиенту 200 с обрезанным телом.
func render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("шаблон %s: %v", name, err)
		http.Error(w, "ошибка рендера страницы", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}
