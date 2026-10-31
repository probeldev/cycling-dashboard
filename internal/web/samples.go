package web

import (
	"fmt"
	"net/url"
	"strconv"
)

const (
	defaultSampleMax = 500    // столько точек график просил раньше
	sampleMaxLimit   = 200000 // потолок: тренировка длиной в сутки при 1 Гц — это ~86k сэмплов
)

// samplesQuery — параметры GET /api/workout/{id}/samples.
type samplesQuery struct {
	Max int  // до скольки точек усреднять
	Raw bool // отдать все сэмплы как есть, без бакет-усреднения
}

// parseSamplesQuery разбирает ?max=N и ?raw=1. Ошибка возвращается для
// ответа 400 — некорректные параметры не должны ни паниковать, ни молча
// превращаться в дефолт.
func parseSamplesQuery(q url.Values) (samplesQuery, error) {
	p := samplesQuery{Max: defaultSampleMax}
	if v := q.Get("max"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("max=%q: нужно целое число", v)
		}
		if n < 1 {
			return p, fmt.Errorf("max=%d: нужно не меньше 1", n)
		}
		if n > sampleMaxLimit {
			return p, fmt.Errorf("max=%d: нужно не больше %d", n, sampleMaxLimit)
		}
		p.Max = n
	}
	if v := q.Get("raw"); v != "" {
		raw, err := strconv.ParseBool(v)
		if err != nil {
			return p, fmt.Errorf("raw=%q: нужно 1 или 0", v)
		}
		p.Raw = raw
	}
	return p, nil
}
