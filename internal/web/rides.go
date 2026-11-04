package web

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

// rideView — данные для шаблона /rides: меню годов + выбранный год
// с enriched поездками.
type rideView struct {
	Years    []rideYear // меню годов: год и сводка, без месяцев
	Selected *rideYear  // раскрытый год: месяцы и поездки
}

type rideYear struct {
	Year    int
	Outdoor store.Agg
	Trainer store.Agg
	Months  []rideMonth
	Active  bool // подсветка года в меню годов
}

type rideMonth struct {
	Month         time.Month
	Outdoor       store.Agg
	Trainer       store.Agg
	TotalCalories int
	TSS           float64
	AvgIF         float64
	Rides         []rideRow
	VolumeSpark   string // inline-SVG bars: TSS по дням месяца
	Open          bool   // раскрыт при загрузке страницы
}

type rideRow struct {
	ID           int64
	StartTime    time.Time
	Date         string
	Title        string
	DistanceKm   float64
	DurationSec  int64
	DurationText string
	AvgPower     int
	TSSText      string
	Trainer      bool
	HasRecord    bool
	PowerSpark   string // inline-SVG polyline points
	Search       string
}

// buildRidesView собирает enriched список поездок для /rides. wantYear —
// параметр ?year=: если он непуст и есть в дереве — это выбранный год,
// иначе год с самыми свежими данными (ListTree сортирует тренировки по
// времени DESC, годы идут от свежих к старым).
func buildRidesView(db *sql.DB, wantYear int) (*rideView, error) {
	years, err := store.ListTree(db)
	if err != nil {
		return nil, err
	}
	records, err := recordWorkoutIDs(db)
	if err != nil {
		return nil, err
	}

	out := &rideView{Years: make([]rideYear, 0, len(years))}
	if len(years) == 0 {
		return out, nil
	}
	sel := 0
	if wantYear > 0 {
		for i, y := range years {
			if y.Year == wantYear {
				sel = i
				break
			}
		}
	}
	for i, y := range years {
		out.Years = append(out.Years, rideYear{
			Year: y.Year, Outdoor: y.Outdoor, Trainer: y.Trainer, Active: i == sel,
		})
	}

	// список поездок показывается только у выбранного года — метрики и
	// сэмплы читаем пачками лишь для него два запросами, а не N+1.
	src := years[sel]
	selYear := &rideYear{
		Year: src.Year, Outdoor: src.Outdoor, Trainer: src.Trainer,
		Months: make([]rideMonth, 0, len(src.Months)),
	}
	ids := make([]int64, 0)
	for _, m := range src.Months {
		for _, r := range m.Rides {
			ids = append(ids, r.ID)
		}
	}
	metas, err := loadRideWorkouts(db, ids)
	if err != nil {
		return nil, err
	}
	powerIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		if metas[id].hasPower {
			powerIDs = append(powerIDs, id)
		}
	}
	samples, err := loadRideSamples(db, powerIDs, 60)
	if err != nil {
		return nil, err
	}

	for _, m := range src.Months {
		rm := rideMonth{
			Month:         m.Month,
			Outdoor:       m.Outdoor,
			Trainer:       m.Trainer,
			TotalCalories: m.TotalCalories,
			TSS:           m.TSS,
			AvgIF:         m.AvgIF,
			Rides:         make([]rideRow, 0, len(m.Rides)),
		}
		for _, r := range m.Rides {
			rm.Rides = append(rm.Rides, buildRideRow(r, metas[r.ID], samples[r.ID], records[r.ID]))
		}
		rm.VolumeSpark = monthVolumeSpark(rm.Rides)
		selYear.Months = append(selYear.Months, rm)
	}
	// какой месяц раскрыт по умолчанию — решает Go, не шаблон
	if i := openMonthIndex(*selYear, time.Now()); i >= 0 {
		selYear.Months[i].Open = true
	}
	out.Selected = selYear
	return out, nil
}

// openMonthIndex — месяц, раскрытый при загрузке: в текущем году это текущий
// календарный месяц, если в нём есть поездки; в любом другом случае самый
// поздний месяц года с поездками (пустые месяцы не открываем).
func openMonthIndex(ry rideYear, now time.Time) int {
	latest := -1
	for i, m := range ry.Months {
		if len(m.Rides) == 0 {
			continue
		}
		if latest < 0 || m.Month > ry.Months[latest].Month {
			latest = i
		}
	}
	if ry.Year == now.Year() {
		for i, m := range ry.Months {
			if m.Month == now.Month() && len(m.Rides) > 0 {
				return i
			}
		}
	}
	return latest
}

// rideWorkoutMeta — только те поля тренировки, которые нужны строке списка:
// достаточно для авто-названия и выбора источника сэмплов.
type rideWorkoutMeta struct {
	hasPower bool
	ftp      int
	iff      float64
	hasIF    bool
	tss      float64
	hasTSS   bool
	vi       float64
	hasVI    bool
}

// loadRideWorkouts читает метаданные всех поездок одним запросом.
func loadRideWorkouts(db *sql.DB, ids []int64) (map[int64]rideWorkoutMeta, error) {
	out := make(map[int64]rideWorkoutMeta, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT id, avg_power, ftp_w, if, tss, vi
	    FROM workouts WHERE id IN (`+sqlPlaceholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ap, ftp sql.NullInt64
		var iff, tss, vi sql.NullFloat64
		if err := rows.Scan(&id, &ap, &ftp, &iff, &tss, &vi); err != nil {
			return nil, err
		}
		out[id] = rideWorkoutMeta{
			hasPower: ap.Valid, ftp: int(ftp.Int64),
			iff: iff.Float64, hasIF: iff.Valid,
			tss: tss.Float64, hasTSS: tss.Valid,
			vi: vi.Float64, hasVI: vi.Valid,
		}
	}
	return out, rows.Err()
}

// buildRideRow обогащает одну поездку: авто-название, спарклайн, рекорд.
func buildRideRow(r store.WorkoutRow, meta rideWorkoutMeta, samples []store.Sample, hasRecord bool) rideRow {
	wo := &store.Workout{
		ID:        r.ID,
		StartTime: r.StartTime,
		Name:      r.Name,
		Duration:  time.Duration(r.DurationSec) * time.Second,
		Trainer:   r.Trainer,
		HasPower:  meta.hasPower,
		FTP:       meta.ftp,
		IF:        meta.iff,
		HasIF:     meta.hasIF,
		TSS:       meta.tss,
		HasTSS:    meta.hasTSS,
		VI:        meta.vi,
		HasVI:     meta.hasVI,
	}

	row := rideRow{
		ID:           r.ID,
		StartTime:    r.StartTime,
		Date:         r.StartTime.Format("02.01.2006"),
		DistanceKm:   r.DistanceKm,
		DurationSec:  r.DurationSec,
		DurationText: formatDurationShort(r.DurationSec),
		AvgPower:     r.AvgPower,
		Trainer:      r.Trainer,
		HasRecord:    hasRecord,
		Search:       fmt.Sprintf("%s %s", r.StartTime.Format("02.01.2006"), r.Name),
	}
	if r.TSS > 0 {
		row.TSSText = fmt.Sprintf("%.0f TSS", math.Round(r.TSS))
	}

	if meta.hasPower {
		row.PowerSpark = powerSparkPoints(samples)
		zones, _ := ridedata.PowerTime(samples, meta.ftp)
		row.Title = autoTitle(wo, zones)
	} else {
		row.Title = fallbackTitle(wo)
	}
	return row
}

// loadRideSamples — batch-версия store.Samples для списка поездок: те же
// бакеты по 60 точек и те же агрегаты, но одним запросом на всю страницу.
func loadRideSamples(db *sql.DB, ids []int64, maxN int) (map[int64][]store.Sample, error) {
	out := make(map[int64][]store.Sample, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if maxN < 1 {
		maxN = 1
	}
	args := make([]any, 0, 2*len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, maxN, maxN)
	ph := sqlPlaceholders(len(ids))
	rows, err := db.Query(`WITH counts AS (
	      SELECT workout_id, COUNT(*) AS total FROM samples
	      WHERE workout_id IN (`+ph+`) GROUP BY workout_id
	    ), numbered AS (
      SELECT workout_id, ts_offset, lat, lon, power, cadence, hr, speed_ms,
             altitude_m, distance_m,
             ROW_NUMBER() OVER (PARTITION BY workout_id ORDER BY ts_offset, rowid) - 1 AS rn
      FROM samples WHERE workout_id IN (`+ph+`)
    )
    SELECT n.workout_id, CAST(ROUND(AVG(n.ts_offset)) AS INTEGER), AVG(n.lat), AVG(n.lon),
           CAST(AVG(n.power) AS INTEGER), CAST(AVG(n.cadence) AS INTEGER), CAST(AVG(n.hr) AS INTEGER),
           AVG(n.speed_ms), AVG(n.altitude_m), MAX(n.distance_m)
	    FROM numbered n JOIN counts c ON c.workout_id = n.workout_id
	    GROUP BY n.workout_id, n.rn / ((c.total + ? - 1) / ?)
	    ORDER BY n.workout_id, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s store.Sample
		var lat, lon, spd, alt, dist sql.NullFloat64
		var p, c, h sql.NullInt64
		if err := rows.Scan(&id, &s.Offset, &lat, &lon, &p, &c, &h, &spd, &alt, &dist); err != nil {
			return nil, err
		}
		s.Lat = lat.Float64
		s.Lon = lon.Float64
		s.HasPos = lat.Valid && lon.Valid
		s.Power = int(p.Int64)
		s.HasPwr = p.Valid
		s.Cadence = int(c.Int64)
		s.HasCad = c.Valid
		s.HR = int(h.Int64)
		s.SpeedMs = spd.Float64
		s.AltitudeM = alt.Float64
		s.HasAlt = alt.Valid
		s.DistanceM = dist.Float64
		s.HasDist = dist.Valid
		out[id] = append(out[id], s)
	}
	return out, rows.Err()
}

func sqlPlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// recordWorkoutIDs возвращает workout_id, у которых есть хотя бы один рекорд
// all-time. Достаточно одного запроса для всего списка.
func recordWorkoutIDs(db *sql.DB) (map[int64]bool, error) {
	records, err := store.BestRecordsUntil(db, time.Time{}, time.Now())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(records))
	for _, r := range records {
		out[r.WorkoutID] = true
	}
	return out, nil
}

// autoTitle — детерминированные правила человеческого названия тренировки.
// Факты: зоны, IF, TSS, VI, длительность. Не использует LLM.
func autoTitle(w *store.Workout, zones [7]float64) string {
	total := 0.0
	for _, s := range zones {
		total += s
	}
	z4plus := 0.0
	z2 := 0.0
	z1z2 := 0.0
	if total > 0 {
		for i := 3; i < len(zones); i++ {
			z4plus += zones[i]
		}
		z2 = zones[1]
		z1z2 = zones[0] + zones[1]
		z4plus = z4plus / total * 100
		z2 = z2 / total * 100
		z1z2 = z1z2 / total * 100
	}

	hours := w.Duration.Hours()
	if w.HasIF && w.IF > 0.95 && z4plus > 30 {
		return "Пороговая работа"
	}
	if hours >= 2.5 && z1z2 > 70 {
		return "Длинная выносливость"
	}
	if z2 > 50 && w.HasVI && w.VI < 1.05 {
		return "Ровное Z2"
	}
	if w.HasTSS && w.TSS < 40 && w.HasIF && w.IF < 0.75 {
		return "Восстановление"
	}
	return fallbackTitle(w)
}

// fallbackTitle — текущее имя или тип тренировки.
func fallbackTitle(w *store.Workout) string {
	if strings.TrimSpace(w.Name) != "" {
		return w.Name
	}
	if w.Trainer {
		return "Станок"
	}
	return "Улица"
}

// powerSparkPoints — points для SVG-polyline 60×16 пикселей по сэмплам мощности.
func powerSparkPoints(samples []store.Sample) string {
	if len(samples) == 0 {
		return ""
	}
	values := make([]int, 0, len(samples))
	maxV := 0
	for _, s := range samples {
		if !s.HasPwr {
			continue
		}
		values = append(values, s.Power)
		if s.Power > maxV {
			maxV = s.Power
		}
	}
	if len(values) == 0 {
		return ""
	}
	var b strings.Builder
	for i, v := range values {
		x := 0.0
		if len(values) > 1 {
			x = float64(i) * 60 / float64(len(values)-1)
		}
		y := 8.0
		if maxV > 0 {
			y = 15 - float64(v)/float64(maxV)*14
		}
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(b.String())
}

// monthVolumeSpark — inline SVG-бары объёма (TSS) по дням месяца.
// Ширина 90px, высота 16px, до 31 столбца. Пустые дни — дорожка.
func monthVolumeSpark(rides []rideRow) string {
	if len(rides) == 0 {
		return ""
	}
	daysInMonth := time.Date(rides[0].StartTime.Year(), rides[0].StartTime.Month()+1, 0, 0, 0, 0, 0, time.Local).Day()
	tssByDay := make([]float64, daysInMonth)
	maxTSS := 0.0
	for _, r := range rides {
		if r.TSSText == "" {
			continue
		}
		// TSSText формата "123 TSS" — извлекаем число.
		var tss float64
		fmt.Sscanf(r.TSSText, "%f", &tss)
		day := r.StartTime.Day() - 1
		if day >= 0 && day < daysInMonth {
			tssByDay[day] += tss
			if tssByDay[day] > maxTSS {
				maxTSS = tssByDay[day]
			}
		}
	}
	if maxTSS <= 0 {
		return ""
	}
	barW := 90.0 / float64(daysInMonth)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg width="90" height="16" viewBox="0 0 90 16" preserveAspectRatio="none" aria-hidden="true">`)
	for i, v := range tssByDay {
		h := 0.0
		if maxTSS > 0 {
			h = v / maxTSS * 14
		}
		x := float64(i) * barW
		y := 15 - h
		fill := "var(--brand)"
		if v == 0 {
			fill = "var(--track)"
		}
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="1" fill="%s"/>`, x, y, barW-0.5, h, fill)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// formatHours — "12 ч" или "1.5 ч" для агрегатов.
func formatHours(h float64) string {
	if h <= 0 {
		return ""
	}
	if h >= 10 {
		return fmt.Sprintf("%.0f ч", math.Round(h))
	}
	return fmt.Sprintf("%.1f ч", h)
}

// humanMin — минуты в читаемый вид: "5 ч 35 мин", а меньше часа — "45 мин".
func humanMin(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d мин", minutes)
	}
	return fmt.Sprintf("%d ч %d мин", minutes/60, minutes%60)
}

// formatDurationShort — всегда "H:MM" для времени в секундах: "1:30", "0:45".
func formatDurationShort(sec int64) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	return fmt.Sprintf("%d:%02d", h, m)
}
