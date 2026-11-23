// Package sysstat le o uso da maquina (processador, memoria, disco) no
// Linux, direto do /proc e do statfs, sem programa externo.
package sysstat

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sample e uma leitura do sistema. CPUBusy e CPUTotal sao contadores
// acumulados desde o boot: o uso do processador sai da diferenca entre duas
// leituras (CPUPercent).
type Sample struct {
	CPUBusy  uint64 `json:"cpu_busy"`
	CPUTotal uint64 `json:"cpu_total"`
	CPUs     int    `json:"cpus"`
	// Load sao as medias de carga de 1, 5 e 15 minutos.
	Load      [3]float64    `json:"load"`
	MemTotal  uint64        `json:"mem_total"`
	MemUsed   uint64        `json:"mem_used"`
	SwapTotal uint64        `json:"swap_total"`
	SwapUsed  uint64        `json:"swap_used"`
	Uptime    time.Duration `json:"uptime"`
	Disks     []Disk        `json:"disks,omitempty"`
}

// Disk e o uso de um sistema de arquivos montado em Path.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
}

// Percent e o uso em porcentagem (0 sem total).
func (d Disk) Percent() float64 { return percent(d.Used, d.Total) }

// MemPercent e o uso da memoria em porcentagem.
func (s Sample) MemPercent() float64 { return percent(s.MemUsed, s.MemTotal) }

// SwapPercent e o uso do swap em porcentagem.
func (s Sample) SwapPercent() float64 { return percent(s.SwapUsed, s.SwapTotal) }

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(used) / float64(total)
}

// CPUPercent e o uso do processador entre duas leituras (0 a 100), ou -1
// quando nao da pra calcular (contadores iguais ou que voltaram).
func CPUPercent(prev, cur Sample) float64 {
	if cur.CPUTotal <= prev.CPUTotal || cur.CPUBusy < prev.CPUBusy {
		return -1
	}
	p := 100 * float64(cur.CPUBusy-prev.CPUBusy) / float64(cur.CPUTotal-prev.CPUTotal)
	return min(100, max(0, p))
}

// Read le o sistema: proc e a raiz do /proc ("/proc" na maquina), e paths
// sao os caminhos cujo disco entra na leitura (um por sistema de arquivos:
// caminhos no mesmo disco aparecem so uma vez, pelo primeiro).
func Read(proc string, paths ...string) (Sample, error) {
	var s Sample
	if err := readCPU(filepath.Join(proc, "stat"), &s); err != nil {
		return s, err
	}
	if err := readMem(filepath.Join(proc, "meminfo"), &s); err != nil {
		return s, err
	}
	if b, err := os.ReadFile(filepath.Join(proc, "loadavg")); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			s.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if b, err := os.ReadFile(filepath.Join(proc, "uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			sec, _ := strconv.ParseFloat(f[0], 64)
			s.Uptime = time.Duration(sec) * time.Second
		}
	}
	seen := map[uint64]bool{}
	for _, p := range paths {
		d, dev, err := statDisk(p)
		if err != nil || seen[dev] {
			continue
		}
		seen[dev] = true
		s.Disks = append(s.Disks, d)
	}
	return s, nil
}

// readCPU soma a linha "cpu" do /proc/stat: ocupado e tudo menos idle e
// iowait; os nucleos sao as linhas "cpuN".
func readCPU(path string, s *Sample) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	found := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			s.CPUs++
			continue
		}
		found = true
		// user nice system idle iowait irq softirq steal (guest ja esta em user)
		for i, v := range fields[1:min(len(fields), 9)] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			s.CPUTotal += n
			if i != 3 && i != 4 {
				s.CPUBusy += n
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New(path + ": sem a linha cpu")
	}
	return nil
}

// readMem: usado e o total menos o disponivel (MemAvailable: o que da pra
// usar sem swap, contando o cache que o kernel solta).
func readMem(path string, s *Sample) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	kb := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		kb[k] = n * 1024
	}
	if kb["MemTotal"] == 0 {
		return errors.New(path + ": sem MemTotal")
	}
	avail, ok := kb["MemAvailable"]
	if !ok { // kernel antigo
		avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
	}
	s.MemTotal = kb["MemTotal"]
	s.MemUsed = s.MemTotal - min(avail, s.MemTotal)
	s.SwapTotal = kb["SwapTotal"]
	s.SwapUsed = s.SwapTotal - min(kb["SwapFree"], s.SwapTotal)
	return nil
}
