package compose

import (
	"fmt"
	"sort"
)

// Service e o que o cais precisa saber de um service do compose do app.
type Service struct {
	Name  string
	Image string
	// Networks sao as redes declaradas no service. Vazio quer dizer que o
	// service nao declarou nenhuma e fica so na default implicita.
	Networks []string
}

// Services lista os services do compose, em ordem alfabetica. Chame depois
// de Validate: aqui so se le, nao se confere seguranca.
func Services(data []byte) ([]Service, error) {
	doc, err := parse(data)
	if err != nil {
		return nil, err
	}
	raw, ok := doc["services"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("compose sem services")
	}
	var out []Service
	for name, v := range raw {
		m, _ := v.(map[string]any)
		s := Service{Name: name}
		s.Image, _ = m["image"].(string)
		// redes em lista ([a, b]) ou em mapa (a: {aliases: ...})
		switch n := m["networks"].(type) {
		case []any:
			for _, x := range n {
				if str, ok := x.(string); ok {
					s.Networks = append(s.Networks, str)
				}
			}
		case map[string]any:
			for k := range n {
				s.Networks = append(s.Networks, k)
			}
			sort.Strings(s.Networks)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
