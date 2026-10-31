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
	Secs  int    `json:"secs"`
}

// distEntry — бакет распределения. Проценты заполняются только для мощности:
// Pct — от времени педалирования (прежняя база, существующий UI не ломаем),
// PctTotal — от общего времени вместе с накатом.
type distEntry struct {
	Label    string  `json:"label"`
	Secs     int     `json:"secs"`
	Pct      float64 `json:"pct,omitempty"`
	PctTotal float64 `json:"pctTotal,omitempty"`
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

// pauseGapSec — разрыв между сэмплами больше этого означает авто-паузу:
// велосипед уже стоит. В накат такое время не пишем — стоянка не равна качению.
const pauseGapSec = 5

// ranges — типичные диапазоны для шоссейника; последний бакет «и выше».
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

// sampleSecs — время сэмпла: интервал до следующего сэмпла, поэтому паузы
// не размазываются по бакетам. Для бакета наката разрыв-автопауза даёт одну
// секунду: после остановки секунды стоянки накатом считать нельзя.
func sampleSecs(samples []store.Sample, i int, coast bool) int {
	dt := int64(1)
	if i+1 < len(samples) {
		dt = samples[i+1].Offset - samples[i].Offset
	}
	if dt <= 0 || dt > 60 {
		dt = 1
	}
	if coast && dt > pauseGapSec {
		dt = 1
	}
	return int(dt)
}

// distributions считает время в каждом диапазоне. Если задан FTP (штамп
// тренировки), мощность режется на зоны Коггана, иначе — по фиксированным
// диапазонам. Нулевая мощность попадает в отдельный бакет наката, плюс
// считаются обе базы времени — педалирование и педалирование вместе с накатом.
func distributions(samples []store.Sample, ftp int) dist {
	out := dist{Buckets: map[string][]distEntry{}}
	for kind, buckets := range distDefs {
		if kind == "power" {
			buckets = powerBuckets(ftp)
		}
		secs := make([]int, len(buckets))
		var hasData bool
		for i, s := range samples {
			j := bucketIndex(kind, buckets, s)
			if j < 0 {
				continue
			}
			hasData = true
			secs[j] += sampleSecs(samples, i, kind == "power" && j == 0)
		}
		if !hasData {
			continue
		}
		entries := make([]distEntry, len(buckets))
		for j, b := range buckets {
			entries[j] = distEntry{Label: b.Label, Secs: secs[j]}
		}
		if kind == "power" {
			out.PowerBase = powerBase{
				CoastSecs: secs[0],
				PedalSecs: sum(secs) - secs[0],
			}
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

func sum(v []int) int {
	total := 0
	for _, x := range v {
		total += x
	}
	return total
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
