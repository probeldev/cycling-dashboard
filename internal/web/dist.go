package web

import (
	"fmt"
	"math"
	"strconv"

	"cycling-dashboard/internal/ridedata"
	"cycling-dashboard/internal/store"
)

type bucket struct {
	Label string
	From  float64
}

// distEntry — бакет распределения времени по диапазону.
type distEntry struct {
	Label string
	Secs  int
	Pct   float64
}

// coastLabel — бакет наката: нулевая мощность при работающем поверметре.
const coastLabel = "0 Вт · накат"

// distDefs — типичные бакеты для шоссейника; последний бакет «и выше».
// Мощность сюда не входит: она считается зонами Коггана (zoneBuckets).
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

// zoneBuckets — зоны Коггана в ваттах. Границы берутся из ridedata, чтобы UI и
// расчёт зон (импорт, workout_zones) не разъезжались. Не округляем до целых:
// 0.56*156 = 87.36, и подпись «87» врала бы; целые пороги печатаем как целые,
// дробные — с одним знаком. Правая граница зоны не включается.
func zoneBuckets(ftp int) []bucket {
	bounds := ridedata.ZoneBounds()
	out := make([]bucket, len(bounds))
	for i, from := range bounds {
		fromW := from * float64(ftp)
		label := fmt.Sprintf("Z%d · %s+", i+1, watts(fromW))
		if i+1 < len(bounds) {
			label = fmt.Sprintf("Z%d · %s–%s", i+1, watts(fromW), watts(bounds[i+1]*float64(ftp)))
		}
		out[i] = bucket{Label: label, From: fromW}
	}
	return out
}

// watts — порог зоны без ложной точности: 112, а не «112», если 112.0,
// и 87.4, если порог 87.36.
func watts(v float64) string {
	// 0.56*200 в float64 — это 112.00000000000001, поэтому сравниваем с допуском
	if r := math.Round(v); math.Abs(v-r) < 0.05 {
		return strconv.Itoa(int(r))
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
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

// zoneDistribution — бакеты мощности по зонам Коггана через ridedata.PowerTime
// (единый расчёт с импортом). Первый бакет — накат, дальше Z1–Z7.
func zoneDistribution(samples []store.Sample, ftp int) ([]distEntry, bool) {
	zones, coast := ridedata.PowerTime(samples, ftp)
	total := coast
	for _, z := range zones {
		total += z
	}
	if total == 0 {
		return nil, false
	}
	buckets := zoneBuckets(ftp)
	entries := make([]distEntry, len(buckets)+1) // [накат] + Z1..Z7
	entries[0] = distEntry{Label: coastLabel, Secs: int(math.Round(coast))}
	for j, z := range zones {
		entries[j+1] = distEntry{Label: buckets[j].Label, Secs: int(math.Round(z))}
	}
	// проценты зон — от времени педалирования, накат в базу не входит
	fillPercents(entries, 1, sumSecs(entries, 1))
	return entries, true
}

// workoutZoneRow — строка стрипа зон на деталке.
type workoutZoneRow struct {
	N      int // номер зоны 1..7 — для цвета из шкалы
	Label  string
	Time   string
	Pct    string  // % от времени педалирования
	PctNum float64 // тот же процент числом — для ширины бара
}

// workoutZoneRows отбрасывает бакет наката: стрип — только Z1–Z7.
func workoutZoneRows(entries []distEntry) []workoutZoneRow {
	if len(entries) <= 1 {
		return nil
	}
	rows := make([]workoutZoneRow, 0, len(entries)-1)
	for i, e := range entries[1:] {
		rows = append(rows, workoutZoneRow{
			N:      i + 1,
			Label:  e.Label,
			Time:   zoneTimeLabel(e.Secs),
			Pct:    trimFloat(e.Pct),
			PctNum: e.Pct,
		})
	}
	return rows
}

// metricDistributions — время по диапазонам каденса, скорости и пульса. Мощность
// сюда не входит: её бакеты — зоны Коггана (zoneDistribution). Проценты
// считаются от времени самой метрики, чтобы полоска и подсказка сходились.
func metricDistributions(samples []store.Sample) map[string][]distEntry {
	out := make(map[string][]distEntry, len(distDefs))
	times := ridedata.Timings(samples)
	for kind, buckets := range distDefs {
		secs := make([]float64, len(buckets))
		var hasData bool
		for i, s := range samples {
			j := bucketIndex(kind, buckets, s)
			if j < 0 {
				continue
			}
			hasData = true
			secs[j] += times[i].Secs
		}
		if !hasData {
			continue
		}
		entries := make([]distEntry, len(buckets))
		for j, b := range buckets {
			entries[j] = distEntry{Label: b.Label, Secs: int(math.Round(secs[j]))}
		}
		fillPercents(entries, 0, sumSecs(entries, 0))
		out[kind] = entries
	}
	return out
}

// metricStripDefs — порядок и подписи полосок на деталке.
var metricStripDefs = []struct{ Kind, Label, Unit, Class string }{
	{"hr", "Пульс", "уд/мин", "dist-hr"},
	{"cadence", "Каденс", "об/мин", "dist-cadence"},
	{"speed", "Скорость", "км/ч", "dist-speed"},
}

// metricStrip — строка полоски распределения времени; легенда под полоской,
// детализация — в title каждого сегмента.
type metricStrip struct {
	Label   string
	Unit    string
	Class   string // базовый префикс цветовых классов: dist-hr / dist-cadence / dist-speed
	Entries []metricStripEntry
}

type metricStripEntry struct {
	Label  string
	Class  string // цветовой класс сегмента (dist-hr-1..8 / dist-cadence-1..8 / dist-speed-1..8): по позиции бакета
	Time   string
	Pct    string
	PctNum float64
}

// distShadeClass — цветовой класс сегмента распределения. Каждый бакет метрики
// получает свой заметно различимый цвет по позиции в исходном срезе бакетов
// (индекс считается по полному набору, а не по отфильтрованным непустым).
func distShadeClass(base string, i int) string {
	return fmt.Sprintf("%s-%d", base, i+1)
}

// metricStrips собирает полоски пульса, каденса и скорости. Пустые метрики и
// скорость у станка не показываются.
func metricStrips(distributions map[string][]distEntry, trainer bool) []metricStrip {
	var out []metricStrip
	for _, def := range metricStripDefs {
		if def.Kind == "speed" && trainer {
			continue
		}
		var rows []metricStripEntry
		entries := distributions[def.Kind]
		for j, e := range entries {
			if e.Secs <= 0 {
				continue
			}
			rows = append(rows, metricStripEntry{
				Label:  e.Label,
				Class:  distShadeClass(def.Class, j),
				Time:   zoneTimeLabel(e.Secs),
				Pct:    trimFloat(e.Pct),
				PctNum: e.Pct,
			})
		}
		if len(rows) == 0 {
			continue
		}
		out = append(out, metricStrip{
			Label: def.Label, Unit: def.Unit, Class: def.Class, Entries: rows,
		})
	}
	return out
}

// fillPercents считает долю каждого бакета от total начиная с индекса from.
func fillPercents(entries []distEntry, from, total int) {
	if total <= 0 {
		return
	}
	for j := from; j < len(entries); j++ {
		entries[j].Pct = round1(float64(entries[j].Secs) / float64(total) * 100)
	}
}

func sumSecs(entries []distEntry, from int) int {
	total := 0
	for _, e := range entries[from:] {
		total += e.Secs
	}
	return total
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
