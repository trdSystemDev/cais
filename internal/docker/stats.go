package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ContainerStats e o uso de um container rodando, como o docker stats ve.
type ContainerStats struct {
	Name string `json:"name"`
	// CPU e a soma dos nucleos (200 = dois nucleos inteiros).
	CPU float64 `json:"cpu"`
	// Mem e a memoria usada, em bytes; MemLimit e o limite do container
	// (sem limite: a memoria da maquina).
	Mem      uint64 `json:"mem"`
	MemLimit uint64 `json:"mem_limit"`
}

// Stats le o uso de todos os containers rodando (uma amostra; o docker
// leva uns 2s pra medir).
func Stats(ctx context.Context) ([]ContainerStats, error) {
	out, err := execRunner(ctx, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("docker stats: %w", lastLines(err.Error(), 3))
	}
	return parseStats(out)
}

func parseStats(out string) ([]ContainerStats, error) {
	var list []ContainerStats
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r struct{ Name, CPUPerc, MemUsage string }
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("saida do docker stats: %w", err)
		}
		used, limit, _ := strings.Cut(r.MemUsage, "/")
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(r.CPUPerc), "%"), 64)
		list = append(list, ContainerStats{Name: r.Name, CPU: cpu, Mem: parseBytes(used), MemLimit: parseBytes(limit)})
	}
	return list, nil
}

// parseBytes le os tamanhos do docker stats: "50.2MiB", "1.5GiB", "800kB",
// "0B" (o docker usa as unidades binarias pra memoria).
func parseBytes(s string) uint64 {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil {
				return 0
			}
			return uint64(f * u.mult)
		}
	}
	return 0
}
