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
	Time    string // время за 90 дней или прочерк
}

var powerZoneNames = [7]string{
	"Восстановление", "Выносливость", "Темп", "Порог", "VO2max", "Анаэробная", "Нейромышечная",
}

var powerZonePurposes = [7]string{
	"восстановление", "базовая выносливость", "темп и аэробная работа", "порог, FTP",
	"VO2max", "анаэробная ёмкость", "нейромышечная мощность",
}

// buildPowerZones строит таблицу от тех же границ, что расчёт зон и /dist:
// 0.56×156 = 87.36 печатается как 87.4, а не округляется до 87. Правая
// граница зоны не входит, поэтому проценты показываем как 56–75%, а не 56–76%.
func buildPowerZones(ftp int, secs [7]int) []powerZone {
	bounds := ridedata.ZoneBounds()
	rows := make([]powerZone, len(bounds))
	for i := range bounds {
		from := bounds[i] * float64(ftp)
		wattsRange := watts(from) + "+"
		pct := fmt.Sprintf("%d%%+", int(math.Round(bounds[i]*100)))
		if i+1 < len(bounds) {
			wattsRange = watts(from) + "–" + watts(bounds[i+1]*float64(ftp))
			pct = fmt.Sprintf("%d–%d%%", int(math.Round(bounds[i]*100)), int(math.Round(bounds[i+1]*100))-1)
		}
		rows[i] = powerZone{
			Zone:    i + 1,
			Name:    powerZoneNames[i],
			Range:   wattsRange,
			Pct:     pct,
			Purpose: powerZonePurposes[i],
			Time:    zoneTimeLabel(secs[i]),
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
