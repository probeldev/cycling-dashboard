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

// parseSamplesQuery разбирает ?raw и ?max. raw идёт первым: при raw=1 max
// игнорируется, поэтому мусор в неиспользуемом параметре не должен ронять
// запрос. Ошибка возвращается для ответа 400 — некорректный параметр не должен
// ни паниковать, ни молча превращаться в дефолт.
func parseSamplesQuery(q url.Values) (samplesQuery, error) {
	p := samplesQuery{Max: defaultSampleMax}
	if v := q.Get("raw"); v != "" {
		raw, err := strconv.ParseBool(v)
		if err != nil {
			return p, fmt.Errorf("raw=%q: принимается 1/0 (true/false)", v)
		}
		p.Raw = raw
	}
	if p.Raw {
		return p, nil
	}
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
	return p, nil
}

// tooManySamples — ошибка для raw=1, если сэмплов больше потолка: сотни тысяч
// точек в JSON не нужны ни графику, ни внешнему анализу.
func tooManySamples(n int) error {
	if n > sampleMaxLimit {
		return fmt.Errorf("raw=1: сэмплов %d, больше потолка %d", n, sampleMaxLimit)
	}
	return nil
}
