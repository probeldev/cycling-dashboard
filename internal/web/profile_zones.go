package web

import (
	"fmt"
	"math"

	"cycling-dashboard/internal/ridedata"
)

// zoneHistoryDays — окно накопленного времени в зонах на /profile.
const zoneHistoryDays = 90

// powerZone — строка таблицы мощностных зон профиля.
type powerZone struct {
	Zone    int
	Name    string
	Range   string // ватты, границы из ridedata.ZoneBounds
	Pct     string // % FTP
	Purpose string
	Time    string  // время за 90 дней или прочерк
	HasTime bool    // были ли секунды в зоне за окно
	TimePct float64 // доля зоны от всего времени в зонах (0–100), для мини-бара
}

var powerZoneNames = [7]string{
	"Восстановление", "Выносливость", "Темп", "Порог", "VO2max", "Анаэробная", "Нейромышечная",
}

var powerZonePurposes = [7]string{
	"восстановление", "базовая выносливость", "темп и аэробная работа", "порог, FTP",
	"VO2max", "анаэробная ёмкость", "нейромышечная мощность",
}

// buildPowerZones строит таблицу от тех же границ, что расчёт зон:
// 0.56×156 = 87.36 печатается как 87.4, а не округляется до 87. FTP дробный
// (156.5) сохраняется: int(ftp) сдвигал бы границы и подписи от реальной базы.
// Правая граница зоны не входит, поэтому проценты показываем как 56–75%,
// а не 56–76%.
func buildPowerZones(ftp float64, secs [7]int) []powerZone {
	bounds := ridedata.ZoneBounds()
	rows := make([]powerZone, len(bounds))
	total := 0
	for _, s := range secs {
		total += s
	}
	for i := range bounds {
		from := bounds[i] * ftp
		wattsRange := watts(from) + "+"
		pct := fmt.Sprintf("%d%%+", int(math.Round(bounds[i]*100)))
		if i+1 < len(bounds) {
			wattsRange = watts(from) + "–" + watts(bounds[i+1]*ftp)
			pct = fmt.Sprintf("%d–%d%%", int(math.Round(bounds[i]*100)), int(math.Round(bounds[i+1]*100))-1)
		}
		timePct := 0.0
		if total > 0 {
			timePct = math.Round(float64(secs[i]) / float64(total) * 1000)
			timePct /= 10
		}
		rows[i] = powerZone{
			Zone:    i + 1,
			Name:    powerZoneNames[i],
			Range:   wattsRange,
			Pct:     pct,
			Purpose: powerZonePurposes[i],
			Time:    zoneTimeLabel(secs[i]),
			HasTime: secs[i] > 0,
			TimePct: timePct,
		}
	}
	return rows
}

// zoneTimeLabel — накопленное время зоны; ноль секунд — прочерк, чтобы пустая
// история не выглядела как «0 мин».
func zoneTimeLabel(secs int) string {
	switch {
	case secs <= 0:
		return "—"
	case secs < 60:
		return fmt.Sprintf("%d с", secs)
	case secs < 3600:
		return fmt.Sprintf("%d мин", secs/60)
	default:
		return fmt.Sprintf("%d ч %d мин", secs/3600, secs%3600/60)
	}
}
