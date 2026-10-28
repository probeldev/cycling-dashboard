package web

import (
	"cycling-dashboard/internal/store"
)

type bucket struct {
	Label string `json:"label"`
	From  float64
	Secs  int    `json:"secs"`
}

type distEntry struct {
	Label string `json:"label"`
	Secs  int    `json:"secs"`
}

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

// distributions считает время в каждом диапазоне. Время сэмпла — интервал
// до следующего сэмпла, поэтому паузы не размазываются по бакетам.
func distributions(samples []store.Sample) map[string][]distEntry {
	out := map[string][]distEntry{}
	for kind, buckets := range distDefs {
		secs := make([]int, len(buckets))
		var hasData bool
		for i, s := range samples {
			v := value(s, kind)
			if v <= 0 {
				continue
			}
			hasData = true
			dt := int64(1)
			if i+1 < len(samples) {
				dt = samples[i+1].Offset - s.Offset
			}
			if dt <= 0 || dt > 60 {
				dt = 1
			}
			for j := len(buckets) - 1; j >= 0; j-- {
				if v >= buckets[j].From {
					secs[j] += int(dt)
					break
				}
			}
		}
		if !hasData {
			continue
		}
		entries := make([]distEntry, len(buckets))
		for j, b := range buckets {
			entries[j] = distEntry{Label: b.Label, Secs: secs[j]}
		}
		out[kind] = entries
	}
	return out
}
