package host

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// UpgradeEvery e de quanto em quanto tempo atualizar os pacotes que nao sao
// de seguranca (o resto do sistema e o Docker): os de seguranca ja vem
// sozinhos, todo dia, pelo unattended-upgrades.
const UpgradeEvery = 30 * 24 * time.Hour

// aptHistory sao os historicos do apt: o do mes e o anterior (o logrotate
// comprime os mais velhos).
var aptHistory = []string{"/var/log/apt/history.log", "/var/log/apt/history.log.1.gz"}

// a linha de comando de uma atualizacao feita a mao (apt, apt-get; upgrade,
// full-upgrade, dist-upgrade)
var manualUpgrade = regexp.MustCompile(`(^|\s)(upgrade|full-upgrade|dist-upgrade)(\s|$)`)

// lastUpgrade e quando foi a ultima atualizacao feita a mao (zero: nenhuma
// nos historicos). As do unattended-upgrades nao contam: so trazem as de
// seguranca.
func lastUpgrade(sys System) time.Time {
	var last time.Time
	for _, path := range aptHistory {
		b, err := sys.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.HasSuffix(path, ".gz") {
			zr, err := gzip.NewReader(bytes.NewReader(b))
			if err != nil {
				continue
			}
			if b, err = io.ReadAll(zr); err != nil {
				continue
			}
		}
		if t := lastUpgradeIn(string(b)); t.After(last) {
			last = t
		}
	}
	return last
}

// lastUpgradeIn le um history.log: blocos separados por linha vazia, com
// Start-Date e Commandline.
func lastUpgradeIn(log string) time.Time {
	var last time.Time
	for _, block := range strings.Split(log, "\n\n") {
		var start time.Time
		cmd := ""
		for _, l := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(l, "Start-Date: "); ok {
				start, _ = time.ParseInLocation("2006-01-02  15:04:05", strings.TrimSpace(v), time.Local)
			}
			if v, ok := strings.CutPrefix(l, "Commandline: "); ok {
				cmd = v
			}
		}
		if start.IsZero() || strings.Contains(cmd, "unattended-upgrade") || !manualUpgrade.MatchString(cmd) {
			continue
		}
		if start.After(last) {
			last = start
		}
	}
	return last
}

// pendingUpgrades conta os pacotes que o full-upgrade atualizaria agora (a
// lista do apt e atualizada todo dia pelo APT::Periodic). Pela simulacao, e
// nao pelo apt list --upgradable: os phased updates do Ubuntu (liberados aos
// poucos) aparecem na lista, mas o full-upgrade ainda nao instala, e o aviso
// nao sairia nem depois de atualizar.
func pendingUpgrades(ctx context.Context, sys System) (int, error) {
	out, err := sys.Run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "full-upgrade")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Inst ") {
			n++
		}
	}
	return n, nil
}

// checkUpgrades: os pacotes que nao sao de seguranca, uma vez por mes. So
// cobra com pacote esperando e a ultima atualizacao a mao ha mais de
// UpgradeEvery.
func checkUpgrades(ctx context.Context, sys System) Item {
	it := Item{ID: "upgrades", Title: "Pacotes do sistema (atualizar uma vez por mes)",
		Fix: "sudo apt update && sudo apt full-upgrade (ou o Corrigir), num horario calmo"}
	n, err := pendingUpgrades(ctx, sys)
	if err != nil {
		it.OK, it.Detail = true, "nao deu pra ver os pacotes (apt): "+firstLine(err)
		return it
	}
	last := lastUpgrade(sys)
	ago := ""
	if !last.IsZero() {
		ago = fmt.Sprintf("; ultima atualizacao ha %d dias", int(time.Since(last).Hours()/24))
	}
	switch {
	case n == 0:
		it.OK, it.Detail = true, "em dia"+ago
	case !last.IsZero() && time.Since(last) < UpgradeEvery:
		it.OK, it.Detail = true, fmt.Sprintf("%d esperando%s; a proxima ate %s", n, ago, last.Add(UpgradeEvery).Local().Format("02/01"))
	case last.IsZero():
		it.Detail = fmt.Sprintf("%d pacotes esperando e nenhuma atualizacao a mao no ultimo mes: hora de atualizar", n)
	default:
		it.Detail = fmt.Sprintf("%d pacotes esperando%s: hora de atualizar", n, ago)
	}
	return it
}

// upgrade atualiza todos os pacotes (o Corrigir de "upgrades"). Se o Docker
// atualizar, os containers reiniciam sozinhos; se o kernel atualizar, a
// checagem pede o reinicio.
func (h *hardener) upgrade(ctx context.Context) (string, bool, error) {
	if err := h.apt(ctx, "update"); err != nil {
		return "", false, err
	}
	before, _ := pendingUpgrades(ctx, h.sys)
	if err := h.apt(ctx, "full-upgrade"); err != nil {
		return "", false, err
	}
	detail := fmt.Sprintf("%d pacotes atualizados", before)
	if h.sys.Exists(rebootRequiredPath) {
		detail += "; falta reiniciar (Reinicio pendente)"
	}
	return detail, true, nil
}
