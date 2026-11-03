package web

import (
	"database/sql"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"cycling-dashboard/internal/store"
)

// todayRoutes — главная «Сегодня» и прежний список поездок на /rides.
func todayRoutes(r chi.Router, db *sql.DB) {
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		st, err := buildStatus(db, time.Now(), false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, "today.html", map[string]any{"S": st})
	})
}

// todayTitle — шапка дашборда: «Сегодня, 1 ноября».
func todayTitle(t time.Time) string {
	months := [...]string{"января", "февраля", "марта", "апреля", "мая", "июня",
		"июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("Сегодня, %d %s", t.Day(), months[t.Month()-1])
}

// insightClass — модификатор .insight по уровню карточки совета: у advice
// «good», у компонента §3 — «--ok».
func insightClass(level string) string {
	switch level {
	case "warn":
		return "insight--warn"
	case "good":
		return "insight--ok"
	case "danger":
		return "insight--danger"
	default:
		return "insight--info"
	}
}

// stateChipClass — модификатор чипа статуса в шапке: цвет = смысл состояния.
func stateChipClass(label string) string {
	switch label {
	case "fresh":
		return "chip--state-ok"
	case "loaded", "lowdata":
		return "chip--state-warn"
	case "overloaded":
		return "chip--state-danger"
	default:
		return "chip--state-brand"
	}
}

// tsbSeriesColor — цвет серии TSB: зелёный при свежести, красный в минусе.
// template.CSS, иначе html/template режет var(--…) в style-атрибуте.
func tsbSeriesColor(v float64) template.CSS {
	if v < 0 {
		return "var(--danger)"
	}
	return "var(--ok)"
}

func kindLabel(kind string) string {
	switch kind {
	case "rest":
		return "Отдых"
	case "z1":
		return "Лёгкая Z1"
	case "z1z2":
		return "Лёгкая Z1–Z2"
	case "z2":
		return "Ровная Z2"
	case "z2long":
		return "Длинная Z2"
	case "ftp_test":
		return "FTP-тест"
	default:
		return "Тренировка"
	}
}

func durationText(d [2]int) string {
	switch {
	case d[0] <= 0 && d[1] <= 0:
		return "—"
	case d[0] == d[1]:
		return fmt.Sprintf("%d мин", d[0])
	default:
		return fmt.Sprintf("%d–%d мин", d[0], d[1])
	}
}

func rangeText(r [2]int) string {
	if r[0] <= 0 && r[1] <= 0 {
		return "—"
	}
	if r[0] == r[1] {
		return fmt.Sprintf("%d", r[0])
	}
	return fmt.Sprintf("%d–%d", r[0], r[1])
}

// wattsText — только число или диапазон: «Вт» добавляет шаблон, чтобы не
// получить «0 Вт», когда FTP не задан.
func wattsText(w *[2]int) string {
	if w == nil {
		return ""
	}
	return rangeText(*w)
}

func cadenceText(c [2]int) string {
	if c[0] <= 0 && c[1] <= 0 {
		return ""
	}
	return "каденс " + rangeText(c)
}

func meterWidth(tss float64, target *float64) int {
	if target == nil || *target <= 0 {
		return 0
	}
	pct := tss / *target * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return int(math.Round(pct))
}

func progressClass(tss float64, target *float64) string {
	if target == nil || *target <= 0 {
		return "red"
	}
	// Округляем процент, иначе 110/100*100 = 110.00000000000001 и правая
	// граница «зелёного» диапазона не срабатывает.
	pct := math.Round(tss / *target * 10000) / 100
	switch {
	case pct >= 90 && pct <= 110:
		return "green"
	case (pct >= 70 && pct < 90) || (pct > 110 && pct <= 125):
		return "yellow"
	default:
		return "red"
	}
}

func signedPct(v *float64) string {
	if v == nil {
		return "—"
	}
	value := *v
	if value == 0 {
		return "+0.0%" // снимает отрицательный нуль, не меняя аргумент
	}
	if value < 0 {
		return fmt.Sprintf("−%.1f%%", math.Abs(value))
	}
	return fmt.Sprintf("+%.1f%%", value)
}

func targetSourceText(s store.TargetSource) string {
	switch s {
	case store.TargetSourceManual:
		return "вручную"
	case store.TargetSourceMedian4:
		return "медиана 4 недель"
	case store.TargetSourceLastFull:
		return "последняя полная неделя"
	default:
		return "не задана"
	}
}
