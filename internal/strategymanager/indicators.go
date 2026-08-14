package strategymanager

import (
	"math"

	"tradingview-bot/internal/domain"
)

// indicatorSeries calcula los valores de un indicador sobre las velas.
func indicatorSeries(ks []domain.Kline, ind Indicator) []float64 {
	source := closeSeries(ks)
	switch ind.Source {
	case "open":
		source = openSeries(ks)
	case "high":
		source = highSeries(ks)
	case "low":
		source = lowSeries(ks)
	case "volume":
		source = volumeSeries(ks)
	}
	switch ind.Type {
	case "sma":
		return sma(source, ind.Length)
	case "ema":
		return ema(source, ind.Length)
	case "rsi":
		return rsi(closeSeries(ks), ind.Length)
	case "atr":
		return atr(ks, ind.Length)
	case "volume_sma":
		return sma(volumeSeries(ks), ind.Length)
	case "donchian":
		return donchianMid(ks, ind.Length)
	}
	return make([]float64, len(ks))
}

func sma(values []float64, length int) []float64 {
	out := make([]float64, len(values))
	if length <= 0 || len(values) == 0 {
		return out
	}
	var sum float64
	for i := 0; i < len(values); i++ {
		sum += values[i]
		if i >= length {
			sum -= values[i-length]
		}
		if i >= length-1 {
			out[i] = sum / float64(length)
		}
	}
	return out
}

func ema(values []float64, length int) []float64 {
	out := make([]float64, len(values))
	if length <= 0 || len(values) == 0 {
		return out
	}
	k := 2.0 / float64(length+1)
	var prev float64
	started := false
	for i := 0; i < len(values); i++ {
		if !started {
			if i < length-1 {
				continue
			}
			// media inicial sobre los primeros `length` valores
			var sum float64
			for j := 0; j < length; j++ {
				sum += values[i-j]
			}
			prev = sum / float64(length)
			out[i] = prev
			started = true
			continue
		}
		prev = values[i]*k + prev*(1-k)
		out[i] = prev
	}
	return out
}

func rsi(values []float64, length int) []float64 {
	out := make([]float64, len(values))
	if length <= 0 || len(values) < length+1 {
		return out
	}
	var avgGain, avgLoss float64
	for i := 1; i <= length; i++ {
		ch := values[i] - values[i-1]
		if ch > 0 {
			avgGain += ch
		} else {
			avgLoss -= ch
		}
	}
	avgGain /= float64(length)
	avgLoss /= float64(length)
	out[length] = 100 - 100/(1+avgGain/maxf(avgLoss, 1e-12))
	for i := length + 1; i < len(values); i++ {
		ch := values[i] - values[i-1]
		gain, loss := 0.0, 0.0
		if ch > 0 {
			gain = ch
		} else {
			loss = -ch
		}
		avgGain = (avgGain*(float64(length)-1) + gain) / float64(length)
		avgLoss = (avgLoss*(float64(length)-1) + loss) / float64(length)
		out[i] = 100 - 100/(1+avgGain/maxf(avgLoss, 1e-12))
	}
	return out
}

func atr(ks []domain.Kline, length int) []float64 {
	out := make([]float64, len(ks))
	if length <= 0 || len(ks) == 0 {
		return out
	}
	tr := make([]float64, len(ks))
	for i, k := range ks {
		tr[i] = k.High - k.Low
		if i > 0 {
			tr[i] = math.Max(tr[i], math.Abs(k.High-ks[i-1].Close))
			tr[i] = math.Max(tr[i], math.Abs(k.Low-ks[i-1].Close))
		}
	}
	prev := 0.0
	started := false
	for i := 0; i < len(ks); i++ {
		if !started {
			if i < length-1 {
				continue
			}
			var sum float64
			for j := 0; j < length; j++ {
				sum += tr[i-j]
			}
			prev = sum / float64(length)
			out[i] = prev
			started = true
			continue
		}
		prev = (prev*(float64(length)-1) + tr[i]) / float64(length)
		out[i] = prev
	}
	return out
}

func donchianMid(ks []domain.Kline, length int) []float64 {
	out := make([]float64, len(ks))
	if length <= 0 {
		return out
	}
	for i := 0; i < len(ks); i++ {
		if i < length-1 {
			continue
		}
		hi := ks[i].High
		lo := ks[i].Low
		for j := i - length + 1; j <= i; j++ {
			if ks[j].High > hi {
				hi = ks[j].High
			}
			if ks[j].Low < lo {
				lo = ks[j].Low
			}
		}
		out[i] = (hi + lo) / 2
	}
	return out
}

func closeSeries(ks []domain.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Close
	}
	return out
}

func openSeries(ks []domain.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Open
	}
	return out
}

func highSeries(ks []domain.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.High
	}
	return out
}

func lowSeries(ks []domain.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Low
	}
	return out
}

func volumeSeries(ks []domain.Kline) []float64 {
	out := make([]float64, len(ks))
	for i, k := range ks {
		out[i] = k.Volume
	}
	return out
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
