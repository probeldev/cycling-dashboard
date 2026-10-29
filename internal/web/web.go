package web

import (
	"database/sql"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/store"
)

//go:embed templates static
var assets embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"km":        func(v float64) string { return trimFloat(v) },
	"hours":     func(h float64) string { return trimFloat(h) },
	"sec2h":     func(sec int64) string { return trimFloat(float64(sec) / 3600) },
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
	"datetime":  func(t time.Time) string { return t.Format("02.01.2006 15:04") },
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
			http.NotFound(w, req)
			return
		}
		render(w, "workout.html", map[string]any{"W": wo})
	})

	r.Post("/workout/{id}/trainer", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		if err := store.SetTrainerOverride(db, id, req.FormValue("value") == "1"); err != nil {
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

	r.Get("/api/workout/{id}/samples", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		samples, err := store.Samples(db, id, 500)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, samples)
	})

	r.Get("/api/workout/{id}/dist", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		wo, err := store.GetWorkout(db, id)
		if err != nil {
			http.NotFound(w, req)
			return
		}
		samples, err := store.Samples(db, id, 100000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, distributions(samples, wo.FTP))
	})

	return r, nil
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}
