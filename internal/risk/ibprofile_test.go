package risk

import (
	"math"
	"testing"

	"tradingview-bot/internal/domain"
)

func TestDefaultIBRiskProfileValid(t *testing.T) {
	p := DefaultIBRiskProfile()
	if err := p.Validate(); err != nil {
		t.Fatalf("perfil por defecto inválido: %v", err)
	}
	if p.StopDefaultPct != IBStopDefaultPct {
		t.Errorf("StopDefaultPct = %.4f, want %.4f", p.StopDefaultPct, IBStopDefaultPct)
	}
	if p.StopMaxPct != IBStopMaxPct {
		t.Errorf("StopMaxPct = %.4f, want %.4f", p.StopMaxPct, IBStopMaxPct)
	}
	if p.MaxDailyLossPct != IBDailyLossDefaultPct {
		t.Errorf("MaxDailyLossPct = %.4f, want %.4f", p.MaxDailyLossPct, IBDailyLossDefaultPct)
	}
}

func TestIBRiskProfileValidateRejectsInvalid(t *testing.T) {
	cases := map[string]func(*IBRiskProfile){
		"risk cero":          func(p *IBRiskProfile) { p.RiskPct = 0 },
		"risk negativo":      func(p *IBRiskProfile) { p.RiskPct = -0.01 },
		"risk > 100%":        func(p *IBRiskProfile) { p.RiskPct = 1.5 },
		"stop default cero":  func(p *IBRiskProfile) { p.StopDefaultPct = 0 },
		"stop default > max": func(p *IBRiskProfile) { p.StopDefaultPct = 0.05 },
		"stop max excede":    func(p *IBRiskProfile) { p.StopMaxPct = 0.03 },
		"daily cero":         func(p *IBRiskProfile) { p.MaxDailyLossPct = 0 },
		"posiciones cero":    func(p *IBRiskProfile) { p.MaxOpenPositions = 0 },
		"rr cero":            func(p *IBRiskProfile) { p.MinRR = 0 },
		"leverage excede":    func(p *IBRiskProfile) { p.MaxLeverage = 21 },
		"leverage cero":      func(p *IBRiskProfile) { p.MaxLeverage = 0 },
	}
	for name, mutate := range cases {
		p := DefaultIBRiskProfile()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: se esperaba error de validación", name)
		}
	}
}

func TestIBRiskProfileClampStopPct(t *testing.T) {
	p := DefaultIBRiskProfile()
	if got := p.ClampStopPct(0); got != IBStopDefaultPct {
		t.Errorf("ClampStopPct(0) = %.4f, want default %.4f", got, IBStopDefaultPct)
	}
	if got := p.ClampStopPct(0.05); got != IBStopMaxPct {
		t.Errorf("ClampStopPct(0.05) = %.4f, want cap %.4f", got, IBStopMaxPct)
	}
	if got := p.ClampStopPct(0.012); got != 0.012 {
		t.Errorf("ClampStopPct(0.012) = %.4f, want 0.012", got)
	}
}

func TestIBRiskProfileSizingSeparatesComponents(t *testing.T) {
	p := DefaultIBRiskProfile()
	balance := 10000.0
	entry := 100.0
	stop := 98.5 // 1.5% por debajo

	if got := p.RiskAmount(balance); math.Abs(got-100) > 1e-9 {
		t.Errorf("RiskAmount = %.4f, want 100", got)
	}
	if got := p.StopDistancePct(entry, stop); math.Abs(got-0.015) > 1e-9 {
		t.Errorf("StopDistancePct = %.4f, want 0.015", got)
	}
	qty := p.PositionSize(balance, entry, stop)
	if math.Abs(qty-66.6667) > 0.01 {
		t.Errorf("PositionSize = %.4f, want ~66.6667", qty)
	}
	exposure := p.Exposure(qty, entry)
	if math.Abs(exposure-6666.67) > 1 {
		t.Errorf("Exposure = %.4f, want ~6666.67", exposure)
	}
	lev := p.Leverage(exposure, balance)
	if math.Abs(lev-0.6667) > 0.001 {
		t.Errorf("Leverage = %.4f, want ~0.6667", lev)
	}
	if !p.WithinLeverage(exposure, balance) {
		t.Error("se esperaba apalancamiento dentro del límite")
	}
	if p.WithinLeverage(exposure*100, balance) {
		t.Error("se esperaba apalancamiento fuera del límite")
	}
}

func TestIBRiskProfileDailyLossLimitBoundaries(t *testing.T) {
	p := DefaultIBRiskProfile()
	acc := domain.Account{Balance: 8000, InitialBalance: 10000}

	if got := p.DailyLossLimit(acc); math.Abs(got-100) > 1e-9 {
		t.Fatalf("DailyLossLimit = %.4f, want 100", got)
	}
	if p.DailyLossExceeded(-99, acc) {
		t.Error("pérdida -99 no debe exceder el límite de 100")
	}
	if !p.DailyLossExceeded(-100, acc) {
		t.Error("pérdida -100 debe alcanzar el límite (justo en el borde)")
	}
	if !p.DailyLossExceeded(-150, acc) {
		t.Error("pérdida -150 debe exceder el límite")
	}
}

func TestIBRiskProfileZeroNegativeSafety(t *testing.T) {
	p := DefaultIBRiskProfile()

	if got := p.RiskAmount(-100); got != 0 {
		t.Errorf("RiskAmount(negativo) = %.2f, want 0", got)
	}
	if got := p.PositionSize(10000, 100, 100); got != 0 {
		t.Errorf("PositionSize con stop == entrada = %.2f, want 0", got)
	}
	if got := p.PositionSize(10000, 0, 90); got != 0 {
		t.Errorf("PositionSize con entrada 0 = %.2f, want 0", got)
	}
	if got := p.Leverage(1000, 0); got != 0 {
		t.Errorf("Leverage con balance 0 = %.2f, want 0", got)
	}
	// Sin base de balance no se puede aplicar el límite -> falla seguro.
	if !p.DailyLossExceeded(-1, domain.Account{}) {
		t.Error("sin balance inicial debe bloquear (fallar de forma segura)")
	}
}
