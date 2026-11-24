package ui

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// maxServices e quantos services aparecem na lista do trafego.
const maxServices = 5

// renderTraffic e o bloco das requisicoes no proxy: a taxa agora com o
// grafico, e ao lado os services que mais recebem.
func (m *model) renderTraffic(w, chartH int) string {
	t := m.th
	muted := t.fg(t.muted)
	tr := m.traffic
	title := t.bold().Render("Requisicoes no proxy")
	switch {
	case tr.Error != "":
		return title + "\n\n" + t.fg(t.warn).Render(truncate("Sem as requisicoes: "+tr.Error, w)) + "\n"
	case tr.At.IsZero():
		return title + "\n\n" + muted.Render("Lendo o proxy...") + "\n"
	}
	right := "total " + count(tr.Total) + " desde que o proxy subiu"
	var b strings.Builder
	b.WriteString(spread(title, muted.Render(right), w) + "\n\n")

	gap := 4
	cw := (w - gap) / 2
	now := "…"
	if tr.Rate >= 0 {
		now = rate(tr.Rate) + "/s"
	}
	head := t.bold().Render("Agora ") + t.fg(t.info).Bold(true).Render(now)
	if tr.Rate > 0 {
		errC := t.muted
		if tr.Errors > 0 {
			errC = t.danger
		}
		head += t.fg(errC).Render(fmt.Sprintf("  erros 5xx %s%%", rate(100*tr.Errors/tr.Rate)))
	}
	top := 1.0
	for _, v := range tr.History {
		top = max(top, v)
	}
	top = niceCeil(top)
	pct := make([]float64, len(tr.History))
	for i, v := range tr.History {
		pct[i] = 100 * v / top
	}
	chart := m.chartScaled(pct, cw, chartH, t.info, rateLabel(top), rateLabel(top/2))
	secs := min(len(tr.History), cw-5) * int(sysEvery.Seconds()*2)
	span := muted.Render(fmt.Sprintf("     por segundo · ultimos %s", spanText(secs)))
	left := head + "\n" + chart + "\n" + span

	var r strings.Builder
	r.WriteString(t.bold().Render("Mais acessados") + muted.Render("  por service do Traefik") + "\n")
	switch {
	case tr.Rate < 0:
		r.WriteString(muted.Render("  medindo..."))
	case len(tr.Services) == 0:
		r.WriteString(muted.Render("  nenhuma requisicao agora"))
	default:
		nameW := 8
		for _, s := range tr.Services[:min(len(tr.Services), maxServices)] {
			nameW = max(nameW, min(24, len(s.Name)))
		}
		barW := max(6, min(30, w-cw-gap-nameW-24))
		for i, s := range tr.Services {
			if i == maxServices {
				r.WriteString(muted.Render(fmt.Sprintf("  +%d", len(tr.Services)-maxServices)))
				break
			}
			line := "  " + fmt.Sprintf("%-*s", nameW, truncate(s.Name, nameW)) + "  " +
				m.barColor(100*s.Rate/math.Max(tr.Services[0].Rate, 0.001), barW, t.info) + "  " +
				t.fg(t.info).Render(fmt.Sprintf("%6s/s", rate(s.Rate)))
			if s.Errors > 0 {
				line += t.fg(t.danger).Render(fmt.Sprintf("  5xx %s/s", rate(s.Errors)))
			}
			r.WriteString(line + "\n")
		}
	}
	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(cw+gap).Render(left),
		strings.TrimSuffix(r.String(), "\n")) + "\n")
	return b.String()
}

// rate escreve uma taxa curta: 0,4 · 12 · 1,2k.
func rate(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.0fk", v/1000)
	case v >= 1000:
		return strings.Replace(fmt.Sprintf("%.1fk", v/1000), ".", ",", 1)
	case v >= 10:
		return fmt.Sprintf("%.0f", v)
	}
	return strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1)
}

// rateLabel e a escala do grafico em 4 colunas.
func rateLabel(v float64) string { return fmt.Sprintf("%4s", rate(v)) }

// count escreve um total com ponto de milhar: 1.234.567.
func count(v float64) string {
	s := fmt.Sprintf("%.0f", v)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// niceCeil arredonda pra cima num valor redondo (1, 2, 5, 10, 20, 50...)
// pro topo do grafico.
func niceCeil(v float64) float64 {
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, f := range []float64{1, 2, 5, 10} {
		if v <= f*p {
			return f * p
		}
	}
	return 10 * p
}

func spanText(secs int) string {
	if secs >= 120 {
		return fmt.Sprintf("%d min", secs/60)
	}
	return fmt.Sprintf("%ds", secs)
}
