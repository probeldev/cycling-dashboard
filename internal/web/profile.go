package web

import (
	"database/sql"
	"net/http"
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
		render(w, "profile.html", map[string]any{
			"History":   history,
			"Weight":    weight,
			"HasWeight": hasWeight,
			"FTP":       int(ftp),
			"HasFTP":    hasFTP,
			"Today":     now.Format("2006-01-02"),
		})
	})

	r.Post("/profile/entry", func(w http.ResponseWriter, req *http.Request) {
		kind := req.FormValue("kind")
		if kind != "weight" && kind != "ftp" {
			http.Error(w, "неизвестный тип", http.StatusBadRequest)
			return
		}
		value, err := strconv.ParseFloat(req.FormValue("value"), 64)
		if err != nil || value <= 0 {
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
