package web

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/importer"
	"cycling-dashboard/internal/store"
)

func profileRoutes(r chi.Router, db *sql.DB) {
	r.Get("/profile", func(w http.ResponseWriter, req *http.Request) {
		history, err := store.ProfileHistory(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		now := time.Now()
		target, err := store.WeeklyTarget(db, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Стартовое значение для формы, когда авто-цель не считается: среднее
		// недельное TSS за последние 4 недели. Расчёт авто-цели не меняем.
		suggestedTarget := 300
		if target.Source == store.TargetSourceNone {
			weeks, err := store.Weeks(db, 4, now)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var sum float64
			active := 0
			for _, week := range weeks {
				if week.TSS > 0 {
					sum += week.TSS
					active++
				}
			}
			if active > 0 {
				v := math.Round(sum / float64(active))
				if v < store.WeeklyTargetMin {
					v = store.WeeklyTargetMin
				}
				if v > store.WeeklyTargetMax {
					v = store.WeeklyTargetMax
				}
				suggestedTarget = int(v)
			}
		}
		weight, hasWeight := store.ProfileAt(db, "weight", now)
		ftp, hasFTP := store.ProfileAt(db, "ftp", now)
		ftpOutdoor, hasFTPOutdoor := store.ProfileAt(db, "ftp_outdoor", now)
		today := store.StartOfDay(now)
		zoneFrom := today.AddDate(0, 0, -(zoneHistoryDays - 1))
		zoneTo := today.AddDate(0, 0, 1)
		var trainerZones, outdoorZones []powerZone
		var trainerZoneDays, outdoorZoneDays int
		if hasFTP {
			secs, err := store.ZoneTime(db, zoneFrom, zoneTo, "trainer")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			trainerZones = buildPowerZones(ftp, secs)
			trainerZoneDays, err = store.ZoneDays(db, zoneFrom, zoneTo, "trainer")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if hasFTPOutdoor {
			secs, err := store.ZoneTime(db, zoneFrom, zoneTo, "outdoor")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			outdoorZones = buildPowerZones(ftpOutdoor, secs)
			outdoorZoneDays, err = store.ZoneDays(db, zoneFrom, zoneTo, "outdoor")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		render(w, "profile.html", map[string]any{
			"History":            history,
			"Weight":             weight,
			"HasWeight":          hasWeight,
			"FTP":                ftp,
			"HasFTP":             hasFTP,
			"FTPOutdoor":         ftpOutdoor,
			"HasFTPOutdoor":      hasFTPOutdoor,
			"TrainerZones":       trainerZones,
			"OutdoorZones":       outdoorZones,
			"TrainerZoneDays":    trainerZoneDays,
			"OutdoorZoneDays":    outdoorZoneDays,
			"Today":              now.Format("2006-01-02"),
			"WeeklyTarget":       target.TSS,
			"HasWeeklyTarget":    target.Source != store.TargetSourceNone,
			"WeeklyTargetSource": targetSourceText(target.Source),
			"SuggestedTarget":    suggestedTarget,
		})
	})

	r.Get("/api/profile/series", func(w http.ResponseWriter, req *http.Request) {
		days, err := parseProfileSeriesDays(req.URL.Query())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		today := store.StartOfDay(time.Now())
		points, err := store.ProfileSeries(db, today.AddDate(0, 0, -(days-1)), today)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, points)
	})

	r.Post("/profile/entry", func(w http.ResponseWriter, req *http.Request) {
		kind := req.FormValue("kind")
		switch kind {
		case "weight", "ftp", "ftp_outdoor":
		default:
			http.Error(w, "неизвестный тип", http.StatusBadRequest)
			return
		}
		value, err := strconv.ParseFloat(req.FormValue("value"), 64)
		if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			http.Error(w, "некорректное значение", http.StatusBadRequest)
			return
		}
		dateRaw := req.FormValue("date")
		// workoutId приходит только от кнопки «Обновить FTP»: запись должна
		// действовать с начала тренировки-источника, а не с момента нажатия.
		var workoutID int64
		var hintKind string
		if raw := req.FormValue("workoutId"); raw != "" {
			switch kind {
			case "ftp":
				hintKind = "trainer"
			case "ftp_outdoor":
				hintKind = "outdoor"
			default:
				http.Error(w, "workoutId допустим только для kind=ftp или kind=ftp_outdoor", http.StatusBadRequest)
				return
			}
			if dateRaw != "" {
				http.Error(w, "date и workoutId нельзя передавать вместе", http.StatusBadRequest)
				return
			}
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "workoutId: нужно положительное целое число", http.StatusBadRequest)
				return
			}
			workoutID = id
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if workoutID != 0 {
			source, err := store.GetWorkout(tx, workoutID)
			if err != nil {
				notFoundOr500(w, req, err)
				return
			}
			switch kind {
			case "ftp":
				if !source.Trainer {
					http.Error(w, "вид подсказки не совпадает с типом тренировки", http.StatusBadRequest)
					return
				}
			case "ftp_outdoor":
				if source.Trainer {
					http.Error(w, "вид подсказки не совпадает с типом тренировки", http.StatusBadRequest)
					return
				}
			}
			if _, err := store.ApplyFTPHintTx(tx, hintKind, value, source); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			at := time.Now()
			if dateRaw != "" {
				t, err := time.ParseInLocation("2006-01-02", dateRaw, time.Local)
				if err != nil {
					http.Error(w, "некорректная дата", http.StatusBadRequest)
					return
				}
				at = t
			}
			if err := store.AddProfileEntryTx(tx, kind, value, at); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// метрики зависят от FTP: считаем их в той же транзакции, чтобы не
		// сохранить профиль с устаревшими IF/TSS
		if _, err := importer.BackfillMetricsTx(tx); err != nil {
			http.Error(w, "не удалось пересчитать метрики: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if workoutID != 0 {
			w.Header().Set("HX-Redirect", "/workout/"+strconv.FormatInt(workoutID, 10))
		} else {
			w.Header().Set("HX-Redirect", "/profile")
		}
	})

	r.Post("/profile/weekly-target", func(w http.ResponseWriter, req *http.Request) {
		value, err := strconv.ParseFloat(req.FormValue("value"), 64)
		if err != nil {
			http.Error(w, "недельная цель: нужно число", http.StatusBadRequest)
			return
		}
		if err := store.SetWeeklyTarget(db, value); err != nil {
			if errors.Is(err, store.ErrWeeklyTargetRange) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/profile")
	})

	r.Post("/profile/weekly-target/reset", func(w http.ResponseWriter, req *http.Request) {
		if err := store.ClearWeeklyTarget(db); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/profile")
	})

	r.Post("/profile/entry/{id}/delete", func(w http.ResponseWriter, req *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(req, "id"), 10, 64)
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if err := store.DeleteProfileEntryTx(tx, id); err != nil {
			if errors.Is(err, store.ErrSyntheticProfileEntry) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := importer.BackfillMetricsTx(tx); err != nil {
			http.Error(w, "не удалось пересчитать метрики: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("HX-Redirect", "/profile")
	})
}

const (
	profileSeriesDefaultDays = 365
	profileSeriesMaxDays     = 3660 // ~10 лет, как у таймлайна
)

// parseProfileSeriesDays разбирает ?days: по умолчанию 365, от 1 до 3660.
func parseProfileSeriesDays(q url.Values) (int, error) {
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("days=%q: нужно целое число", v)
		}
		if n < 1 {
			return 0, fmt.Errorf("days=%d: нужно не меньше 1", n)
		}
		if n > profileSeriesMaxDays {
			return 0, fmt.Errorf("days=%d: не больше %d", n, profileSeriesMaxDays)
		}
		return n, nil
	}
	return profileSeriesDefaultDays, nil
}
