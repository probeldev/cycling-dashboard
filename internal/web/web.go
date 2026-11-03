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
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/advice"
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
	// intFloat — целое без дробной части: дробные TSS на дашборде округляем
	// (TODO-UI §4.1), trimFloat показывал бы «118.0»
	"intFloat": func(v float64) string { return strconv.FormatFloat(math.Round(v), 'f', -1, 64) },
	"watts":    watts,
	"deref": func(v *float64) float64 {
		if v == nil {
			return 0
		}
		return *v
	},
	"derefInt": func(v *int) int {
		if v == nil {
			return 0
		}
		return *v
	},
	"kindLabel":        kindLabel,
	"insightClass":     insightClass,
	"stateChipClass":   stateChipClass,
	"tsbSeriesColor":   tsbSeriesColor,
	"durationText":     durationText,
	"rangeText":        rangeText,
	"wattsText":        wattsText,
	"cadenceText":      cadenceText,
	"meterWidth":       meterWidth,
	"progressClass":    progressClass,
	"signedPct":        signedPct,
	"targetSourceText": targetSourceText,
	"pluralRides":      pluralRides,
	"pluralDays":       pluralDays,
	"ifVerdict":        ifVerdict,
	"viVerdict":        viVerdict,
	"tssVerdict":       tssVerdict,
	"pauseVerdict":     pauseVerdict,
	"insightLevel":     insightLevel,
	"sub":              func(a, b int) int { return a - b },
	"absint":           func(v int) int { return int(math.Abs(float64(v))) },
	"json": func(v any) template.JS {
		// только сгенерированные сервером числа/подписи — безопасно в <script>
		b, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		return template.JS(b)
	},
	"divf":  func(a int, b float64) float64 { return float64(a) / b },
	"divff": func(a, b float64) float64 { return a / b },
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
	"durationShort": formatDurationShort,
	"formatHours":   formatHours,
	"safeHTML":      func(s string) template.HTML { return template.HTML(s) },
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
	todayRoutes(r, db)
	profileRoutes(r, db)
	progressRoutes(r, db)

	r.Get("/rides", func(w http.ResponseWriter, req *http.Request) {
		view, err := buildRidesView(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, "rides.html", map[string]any{"Rides": view})
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
		prs, err := store.WorkoutPRs(db, id, time.Now(), false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bikes, err := store.Bikes(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		hints, err := store.FTPHints(db, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var ftpHint *store.FTPHint
		for i := range hints {
			if hints[i].WorkoutID == id {
				ftpHint = &hints[i]
				break
			}
		}
		advCtx, err := workoutAdviceContext(db, wo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mmpC := mmpCards(mmp, prs)
		// тренд — значение этой тренировки против лучшего того же окна за
		// 30 дней до её даты: запрос тот же, что /api/records?period=30d,
		// но относительно даты тренировки, а не now (§4.3). Верхняя граница
		// — старт тренировки: сама она и всё позднее в прошлый период не
		// попадают.
		prev30, err := store.BestRecords(db,
			store.StartOfDay(wo.StartTime).AddDate(0, 0, -30), wo.StartTime, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		attachRecordTrends(mmpC, prev30)
		// связный вывод (§6): тот же контекст, что у инсайтов, + рекорды
		advCtx.Records = recordFacts(mmpC)
		// зоны — из того же расчёта, что /api/workout/{id}/dist; второй
		// формулы на странице нет
		var powerZones []workoutZoneRow
		if wo.FTP > 0 && wo.HasPower {
			samples, err := store.RawSamples(db, id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if entries, _, ok := zoneDistribution(samples, wo.FTP); ok {
				powerZones = workoutZoneRows(entries)
			}
		}
		render(w, "workout.html", map[string]any{
			"W": wo, "MMP": mmpC, "Bikes": bikes,
			"FTPHint": ftpHint, "Advice": advice.For(*wo, advCtx), "PowerZones": powerZones,
			"Summary": advice.Summary(*wo, advCtx), "ZoneEdges": zoneEdgesW(wo.FTP),
		})
	})

	r.Post("/workout/{id}/test", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		isTest, ok := parseFlagValue(req.FormValue("value"))
		if !ok {
			http.Error(w, "value: нужно 0 или 1", http.StatusBadRequest)
			return
		}
		if err := store.SetWorkoutTest(db, id, isTest); err != nil {
			notFoundOr500(w, req, err)
			return
		}
		w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(id, 10))
	})

	r.Post("/workout/{id}/trainer", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		trainer, ok := parseFlagValue(req.FormValue("value"))
		if !ok {
			http.Error(w, "value: нужно 0 или 1", http.StatusBadRequest)
			return
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if err := store.SetTrainerOverrideTx(tx, id, trainer); err != nil {
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
			notFoundOr500(w, req, err)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, "некорректная форма", http.StatusBadRequest)
			return
		}
		var upd store.WorkoutContextUpdate
		if req.Form.Has("notes") {
			notes := strings.TrimSpace(req.FormValue("notes"))
			if len([]rune(notes)) > maxNotesLen {
				http.Error(w, "заметка длиннее 2000 символов", http.StatusBadRequest)
				return
			}
			upd.Notes = &notes
		}
		if upd.Notes == nil {
			http.Error(w, "нужно передать notes", http.StatusBadRequest)
			return
		}
		if err := store.SetWorkoutContext(db, id, upd); err != nil {
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
		now := time.Now()
		from, to, err := parseTimelineRange(req.URL.Query(), now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		days, err := store.TimelineUntil(db, from, to, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, days)
	})

	r.Get("/api/status", handleStatus(db))

	r.Get("/api/readiness", func(w http.ResponseWriter, req *http.Request) {
		readiness, err := store.Readiness(db, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, readiness)
	})

	r.Get("/api/ftp-hint", func(w http.ResponseWriter, req *http.Request) {
		hints, err := store.FTPHints(db, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// одна подсказка или null: [] клиенту пришлось бы разбирать отдельно
		if len(hints) == 0 {
			writeJSON(w, nil)
			return
		}
		writeJSON(w, hints[0])
	})

	r.Get("/api/records", func(w http.ResponseWriter, req *http.Request) {
		from, to, prevDays, err := parseRecordsPeriod(req.URL.Query(), time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		includeTests, err := parseIncludeTests(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err := store.BestRecordsUntil(db, from, to, includeTests)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if prevDays > 0 {
			prev, err := store.BestRecords(db, from.AddDate(0, 0, -prevDays), from, includeTests)
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
		includeTests, err := parseIncludeTests(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		prs, err := store.WorkoutPRs(db, id, time.Now(), includeTests)
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
		includeTests, err := parseIncludeTests(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		records, err := store.BestRecordsUntil(db, from, to, includeTests)
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

// parseFlagValue — форма переключателя принимает только 0 или 1: пустое
// значение или мусор не должны молча снимать флаг.
func parseFlagValue(raw string) (value bool, ok bool) {
	switch raw {
	case "0":
		return false, true
	case "1":
		return true, true
	default:
		return false, false
	}
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

// writeJSON кодирует ответ в буфер: иначе ошибка json.Encode (например, +Inf
// из битых данных профиля) уходила бы клиенту как 200 с пустым телом.
func writeJSON(w http.ResponseWriter, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		log.Printf("JSON: %v", err)
		http.Error(w, "ошибка кодирования JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(buf.Bytes())
}
