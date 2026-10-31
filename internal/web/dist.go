package web

import (
	"encoding/json"
	"fmt"
	"math"

	"cycling-dashboard/internal/store"
)

type bucket struct {
	Label string `json:"label"`
	From  float64
	Secs  int `json:"secs"`
}

// distEntry — бакет распределения. Проценты ненулевые только для мощности:
// Pct — от времени педалирования (прежняя база, существующий UI не ломаем),
// PctTotal — от общего времени вместе с накатом. Поля есть у всех бакетов
// (у cadence/speed/hr — нули), чтобы клиент мог суммировать без проверок на nil.
type distEntry struct {
	Label    string  `json:"label"`
	Secs     int     `json:"secs"`
	Pct      float64 `json:"pct"`
	PctTotal float64 `json:"pctTotal"`
}

// powerBase — две базы времени распределения мощности. Раньше нулевая мощность
// молча выбрасывалась и зоны считались только «по времени педалирования»,
// из-за чего было непонятно, откуда база.
type powerBase struct {
	CoastSecs int `json:"coastSecs"` // power == 0 при живом поверметре — накат
	PedalSecs int `json:"pedalSecs"` // power > 0 — педалирование
	TotalSecs int `json:"totalSecs"` // pedal + coast
}

// dist — результат распределений. В JSON раскладывается плоско
// ({"power": [...], "cadence": [...], ..., "powerBase": {...}}), чтобы
// dist.power остался массивом и контракт API не сломался.
type dist struct {
	Buckets   map[string][]distEntry
	PowerBase powerBase
}

func (d dist) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(d.Buckets)+1)
	for k, v := range d.Buckets {
		m[k] = v
	}
	if _, ok := d.Buckets["power"]; ok {
		m["powerBase"] = d.PowerBase
	}
	return json.Marshal(m)
}

// coastLabel — бакет наката: нулевая мощность при работающем поверметре.
const coastLabel = "0 Вт · накат"

// distDefs — типичные бакеты для шоссейника; последний бакет «и выше».
// Для мощности при заданном FTP вместо них берутся зоны Коггана (zoneBuckets).
var distDefs = map[string][]bucket{
	"cadence": {
		{Label: "0–40", From: 0},
		{Label: "40–50", From: 40},
		{Label: "50–60", From: 50},
		{Label: "60–70", From: 60},
		{Label: "70–80", From: 70},
		{Label: "80–90", From: 80},
		{Label: "90–100", From: 90},
		{Label: "100+", From: 100},
	},
	"speed": {
		{Label: "0–10", From: 0},
		{Label: "10–15", From: 10},
		{Label: "15–20", From: 15},
		{Label: "20–25", From: 20},
		{Label: "25–30", From: 25},
		{Label: "30–35", From: 30},
		{Label: "35–40", From: 35},
		{Label: "40+", From: 40},
	},
	"power": {
		{Label: "0–50", From: 0},
		{Label: "50–100", From: 50},
		{Label: "100–150", From: 100},
		{Label: "150–200", From: 150},
		{Label: "200–250", From: 200},
		{Label: "250–300", From: 250},
		{Label: "300–400", From: 300},
		{Label: "400+", From: 400},
	},
	"hr": {
		{Label: "0–100", From: 0},
		{Label: "100–120", From: 100},
		{Label: "120–130", From: 120},
		{Label: "130–140", From: 130},
		{Label: "140–150", From: 140},
		{Label: "150–160", From: 150},
		{Label: "160–170", From: 160},
		{Label: "170+", From: 170},
	},
}

// powerZones — зоны Коггана в ваттах от FTP; последний бакет «и выше».
var powerZones = []struct {
	Name string
	From float64 // доля FTP
}{
	{"Z1", 0}, {"Z2", 0.56}, {"Z3", 0.76}, {"Z4", 0.91},
	{"Z5", 1.06}, {"Z6", 1.21}, {"Z7", 1.51},
}

func zoneBuckets(ftp int) []bucket {
	out := make([]bucket, len(powerZones))
	for i, z := range powerZones {
		from := z.From * float64(ftp)
		label := fmt.Sprintf("%s · %d+", z.Name, int(from+0.5))
		if i+1 < len(powerZones) {
			to := powerZones[i+1].From * float64(ftp)
			label = fmt.Sprintf("%s · %d–%d", z.Name, int(from+0.5), int(to+0.5)-1)
		}
		out[i] = bucket{Label: label, From: from}
	}
	return out
}

// powerBuckets — диапазоны мощности (зоны Коггана при заданном FTP, иначе
// фиксированные) с добавленным первым бакетом «0 Вт · накат».
func powerBuckets(ftp int) []bucket {
	base := distDefs["power"]
	if ftp > 0 {
		base = zoneBuckets(ftp)
	}
	out := make([]bucket, 0, len(base)+1)
	out = append(out, bucket{Label: coastLabel, From: 0})
	return append(out, base...)
}

func value(s store.Sample, kind string) float64 {
	switch kind {
	case "cadence":
		return float64(s.Cadence)
	case "speed":
		return s.SpeedMs * 3.6
	case "power":
		return float64(s.Power)
	case "hr":
		return float64(s.HR)
	}
	return 0
}

// bucketIndex — номер бакета для сэмпла, -1 если значение не учитывается.
// Для мощности нуль — это накат (первый бакет), а отсутствие данных
// (дропаут поверметра, HasPwr=false) не учитывается вовсе: это разные вещи.
// Для остальных метрик нули по-прежнему исключаются.
func bucketIndex(kind string, buckets []bucket, s store.Sample) int {
	v := value(s, kind)
	if kind == "power" {
		if !s.HasPwr {
			return -1
		}
		if v == 0 {
			return 0 // накат
		}
	} else if v <= 0 {
		return -1
	}
	for j := len(buckets) - 1; j >= 0; j-- {
		if v >= buckets[j].From {
			return j
		}
	}
	return -1
}

const (
	// pauseGapSec — разрыв между записями больше этого может быть авто-паузой.
	// Не путать с importer.npPauseSec: там тот же порог обрывает окно NP,
	// здесь — решает, приписывать ли время разрыва бакету. Это разные допущения:
	// окно NP должно начинаться заново, а время записи может остаться целым.
	pauseGapSec = 5
	// maxGapSec — потолок времени одной записи при движении (прореженная запись).
	maxGapSec = 60
	// minMovingMs — 1 км/ч в м/с: медленнее считаем, что велосипед стоял.
	minMovingMs = 1.0 / 3.6
)

// sampleSeconds — время каждой записи. Время записи — интервал до следующей;
// записи с одинаковым ts_offset (запись 2 Гц) делят интервал до следующего
// РАЗЛИЧНОГО offset поровну. Разрыв больше pauseGapSec — это либо авто-пауза
// (1 с), либо прореживание записи при движении (полный интервал, но не больше
// maxGapSec). Правило одно для всех метрик — отдельных исключений для наката нет.
func sampleSeconds(samples []store.Sample) []float64 {
	out := make([]float64, len(samples))
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && samples[end].Offset == samples[start].Offset {
			end++
		}
		share := groupSeconds(samples, start, end) / float64(end-start)
		for i := start; i < end; i++ {
			out[i] = share
		}
		start = end
	}
	return out
}

// groupSeconds — время группы записей с одинаковым offset.
func groupSeconds(samples []store.Sample, start, end int) float64 {
	if end >= len(samples) {
		return 1 // после последней записи времени нет
	}
	dt := float64(samples[end].Offset - samples[start].Offset)
	if dt <= 0 {
		return 1 // записи не по порядку — не размазываем
	}
	if dt > pauseGapSec && isPause(samples, start, end) {
		return 1
	}
	return math.Min(dt, maxGapSec)
}

// isPause — разрыв это стоянка, а не прореживание записи (smart recording).
// Приоритет признаков:
//  1. дистанция — если известна с обеих сторон, решает её прирост: перед
//     авто-паузой в записи остаётся последняя «ещё ехавшая» скорость (7–8 км/ч),
//     а пройденное расстояние уже не растёт;
//  2. скорость на границе разрыва — от 1 км/ч ехали (медленнее или нет данных —
//     решает следующий пункт);
//  3. педалирование: мощность или каденс есть — ехали, иначе стояли.
func isPause(samples []store.Sample, start, end int) bool {
	next := samples[end]
	dt := float64(next.Offset - samples[start].Offset)
	if samples[start].HasDist && next.HasDist && dt > 0 {
		return (next.DistanceM-samples[start].DistanceM)/dt < minMovingMs
	}
	for j := max(0, start-1); j <= end; j++ {
		if samples[j].SpeedMs >= minMovingMs {
			return false
		}
	}
	return !pedaling(samples, start, end)
}

// pedaling — в группе записей есть мощность или каденс.
func pedaling(samples []store.Sample, start, end int) bool {
	for i := start; i < end; i++ {
		if samples[i].Power != 0 || samples[i].Cadence != 0 {
			return true
		}
	}
	return false
}

// distributions считает время в каждом диапазоне. Если задан FTP (штамп
// тренировки), мощность режется на зоны Коггана, иначе — по фиксированным
// диапазонам. Нулевая мощность попадает в отдельный бакет наката, плюс
// считаются обе базы времени — педалирование и педалирование вместе с накатом.
func distributions(samples []store.Sample, ftp int) dist {
	out := dist{Buckets: map[string][]distEntry{}}
	times := sampleSeconds(samples)
	for kind, buckets := range distDefs {
		if kind == "power" {
			buckets = powerBuckets(ftp)
		}
		secs := make([]float64, len(buckets))
		var hasData bool
		for i, s := range samples {
			j := bucketIndex(kind, buckets, s)
			if j < 0 {
				continue
			}
			hasData = true
			secs[j] += times[i]
		}
		if !hasData {
			continue
		}
		entries := make([]distEntry, len(buckets))
		for j, b := range buckets {
			entries[j] = distEntry{Label: b.Label, Secs: int(math.Round(secs[j]))}
		}
		if kind == "power" {
			pedal := 0
			for _, e := range entries[1:] {
				pedal += e.Secs
			}
			out.PowerBase = powerBase{CoastSecs: entries[0].Secs, PedalSecs: pedal}
			out.PowerBase.TotalSecs = out.PowerBase.CoastSecs + out.PowerBase.PedalSecs
			fillPercents(entries, out.PowerBase)
		}
		out.Buckets[kind] = entries
	}
	return out
}

// fillPercents проставляет доли бакетов мощности: Pct — от времени
// педалирования (бакет наката не педалирование, у него Pct=0),
// PctTotal — от общего времени. Сумма PctTotal по всем бакетам ≈ 100.
func fillPercents(entries []distEntry, base powerBase) {
	pedal, total := float64(base.PedalSecs), float64(base.TotalSecs)
	for j := range entries {
		if j > 0 && pedal > 0 {
			entries[j].Pct = round1(float64(entries[j].Secs) / pedal * 100)
		}
		if total > 0 {
			entries[j].PctTotal = round1(float64(entries[j].Secs) / total * 100)
		}
	}
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
