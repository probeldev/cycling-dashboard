package web

import (
	"database/sql"
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
		weight, hasWeight := store.ProfileAt(db, "weight", now)
		ftp, hasFTP := store.ProfileAt(db, "ftp", now)
		ftpOutdoor, hasFTPOutdoor := store.ProfileAt(db, "ftp_outdoor", now)
		today := store.StartOfDay(now)
		zoneFrom := today.AddDate(0, 0, -(zoneHistoryDays - 1))
		zoneTo := today.AddDate(0, 0, 1)
		var trainerZones, outdoorZones []powerZone
		if hasFTP {
			secs, err := store.ZoneTime(db, zoneFrom, zoneTo, true)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			trainerZones = buildPowerZones(int(ftp), secs)
		}
		if hasFTPOutdoor {
			secs, err := store.ZoneTime(db, zoneFrom, zoneTo, false)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			outdoorZones = buildPowerZones(int(ftpOutdoor), secs)
		}
		render(w, "profile.html", map[string]any{
			"History":       history,
			"Weight":        weight,
			"HasWeight":     hasWeight,
			"FTP":           int(ftp),
			"HasFTP":        hasFTP,
			"FTPOutdoor":    int(ftpOutdoor),
			"HasFTPOutdoor": hasFTPOutdoor,
			"TrainerZones":  trainerZones,
			"OutdoorZones":  outdoorZones,
			"Today":         now.Format("2006-01-02"),
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
		at := time.Now()
		if d := req.FormValue("date"); d != "" {
			t, err := time.ParseInLocation("2006-01-02", d, time.Local)
			if err != nil {
				http.Error(w, "некорректная дата", http.StatusBadRequest)
				return
			}
			at = t
		}
		tx, err := db.Begin()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		if err := store.AddProfileEntryTx(tx, kind, value, at); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
