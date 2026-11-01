// Package thresholds — единые границы Training Stress Balance и «мало данных»
// для статуса, плана, прогресса и советов. Разные числа в разных пакетах
// давали противоречивые вердикты на одном и том же TSB.
package thresholds

const (
	// TSBFreshAbove — выше этого TSB считается свежим.
	TSBFreshAbove = 10.0
	// TSBNormMin — нижняя граница нормы; ниже начинается загруженность.
	TSBNormMin = -10.0
	// TSBLoadedMin — нижняя граница загруженности; ниже — перегрузка.
	TSBLoadedMin = -25.0
	// TSBLightBelow — порог, ниже которого план уже не даёт качественную
	// работу, а только лёгкую Z1–Z2.
	TSBLightBelow = -15.0

	// MinDataDays и MinLoadDays42 — минимум истории и дней с нагрузкой за
	// 42 дня для уверенной рекомендации.
	MinDataDays   = 21
	MinLoadDays42 = 9
	// DataWindowDays — окно, в котором считаются дни с нагрузкой.
	DataWindowDays = 42
)

// TSBState — класс свежести по единым границам.
type TSBState string

const (
	Fresh      TSBState = "fresh"
	Norm       TSBState = "norm"
	Loaded     TSBState = "loaded"
	Overloaded TSBState = "overloaded"
)

// TSBStateFor классифицирует TSB: > +10 свежий, −10…+10 норма,
// −25…−10 загружен, ниже −25 перегружен.
func TSBStateFor(tsb float64) TSBState {
	switch {
	case tsb > TSBFreshAbove:
		return Fresh
	case tsb >= TSBNormMin:
		return Norm
	case tsb >= TSBLoadedMin:
		return Loaded
	default:
		return Overloaded
	}
}

// LowData — общий предикат «мало данных»: короткая история или слишком мало
// дней с реальной нагрузкой за последние 42 дня.
func LowData(dataDays, loadDays42 int) bool {
	return dataDays < MinDataDays || loadDays42 < MinLoadDays42
}
