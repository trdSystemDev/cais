package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/trdSystemDev/cais/internal/core"
)

type containersMsg struct {
	usage core.ContainerUsage
	err   error
}

// containersCmd pede o uso de cada container (o core le no maximo a cada
// 8s: o docker stats leva uns 2s).
func (m *model) containersCmd() tea.Cmd {
	return func() tea.Msg {
		u, err := m.api.ContainerStats(context.Background())
		return containersMsg{u, err}
	}
}

// consumer e um dono de containers (app, servico ou container solto) e a
// soma do uso deles.
type consumer struct {
	name, kind string
	cpu        float64 // % da maquina
	mem        uint64
}

// consumers soma o uso dos containers por dono: o app ou o servico da
// infra; o que nao e do cais aparece pelo nome do container. Do que mais
// usa o processador pro que menos (empate: a memoria).
func (m *model) consumers() []consumer {
	owner := map[string][2]string{}
	for _, a := range m.state.Apps.List {
		for _, c := range a.Containers {
			owner[c.Name] = [2]string{a.Name, "app"}
		}
	}
	for _, s := range m.state.Infra.Stacks {
		for _, c := range s.Containers {
			owner[c.Name] = [2]string{s.Name, "servico"}
		}
	}
	cpus := float64(max(1, m.sys.Now.CPUs))
	by := map[string]*consumer{}
	var out []*consumer
	for _, c := range m.ctrs.List {
		o, ok := owner[c.Name]
		if !ok {
			o = [2]string{c.Name, "container"}
		}
		key := o[1] + "/" + o[0]
		cs := by[key]
		if cs == nil {
			cs = &consumer{name: o[0], kind: o[1]}
			by[key] = cs
			out = append(out, cs)
		}
		cs.cpu += c.CPU / cpus
		cs.mem += c.Mem
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].cpu != out[j].cpu {
			return out[i].cpu > out[j].cpu
		}
		return out[i].mem > out[j].mem
	})
	list := make([]consumer, len(out))
	for i, c := range out {
		list[i] = *c
	}
	return list
}

// maxConsumers e quantos donos a lista mostra.
const maxConsumers = 5

// renderContainers e a tabela de quem mais usa a maquina, em w colunas.
func (m *model) renderContainers(w int) string {
	t := m.th
	muted := t.fg(t.muted)
	title := t.bold().Render("Quem mais usa") + muted.Render("  processador e memoria, por app e servico")
	switch {
	case m.ctrsErr != "" || m.ctrs.Error != "":
		return title + "\n" + t.fg(t.warn).Render(truncate("  sem o docker stats: "+cmpOrStr(m.ctrsErr, m.ctrs.Error), w))
	case m.ctrs.At.IsZero():
		return title + "\n" + muted.Render("  medindo...")
	}
	list := m.consumers()
	if len(list) == 0 {
		return title + "\n" + muted.Render("  nenhum container rodando")
	}
	nameW := 8
	for _, c := range list[:min(len(list), maxConsumers)] {
		nameW = max(nameW, min(24, len(c.name)))
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	b.WriteString(muted.Render(fmt.Sprintf("  %-*s  %-9s  %5s  %9s", nameW, "", "", "CPU", "memoria")))
	memTotal := float64(max(1, m.sys.Now.MemTotal))
	for i, c := range list {
		if i == maxConsumers {
			b.WriteString("\n" + muted.Render(fmt.Sprintf("  +%d", len(list)-maxConsumers)))
			break
		}
		name := fmt.Sprintf("%-*s", nameW, truncate(c.name, nameW))
		if c.kind == "app" {
			name = m.zones.Mark("dash-app:"+c.name, name)
		}
		memP := 100 * float64(c.mem) / memTotal
		b.WriteString("\n  " + name + "  " + muted.Render(fmt.Sprintf("%-9s", c.kind)) + "  " +
			t.fg(m.level(c.cpu)).Render(fmt.Sprintf("%4.0f%%", c.cpu)) + "  " +
			t.fg(m.level(memP)).Render(fmt.Sprintf("%9s", size(c.mem))))
	}
	return b.String()
}
