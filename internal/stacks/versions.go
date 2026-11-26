package stacks

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/trdSystemDev/cais/internal/config"
)

// Image e a imagem de um servico com versao escolhida pelo usuario (a tag
// fica em config.Versions; vazio e a Default).
type Image struct {
	Repo    string
	Default string
	// Example e o formato da tag, pra ajuda e pro erro
	Example string
	// tag aceita (o formato da imagem, com a versao no comeco)
	format *regexp.Regexp
}

// Images sao os servicos com versao escolhida: os bancos e o Garage. O
// proxy acompanha a versao do cais (o compose dele muda junto).
var Images = map[string]Image{
	"postgres": {Repo: "postgis/postgis", Default: "18-3.6-alpine", Example: "18-3.6-alpine (Postgres 18, PostGIS 3.6)",
		format: regexp.MustCompile(`^(\d+)(\.\d+)?-\d+\.\d+(\.\d+)?(-[a-z0-9.]+)?$`)},
	"mysql": {Repo: "mysql", Default: "9.7", Example: "9.7 ou 8.4.5",
		format: regexp.MustCompile(`^(\d+)\.(\d+)(\.\d+)?(-[a-z0-9.]+)?$`)},
	"garage": {Repo: "dxflrs/garage", Default: "v2.5.0", Example: "v2.5.0",
		format: regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)},
}

// Versioned diz se o servico tem versao escolhida.
func Versioned(name string) bool { _, ok := Images[name]; return ok }

// Version e a tag em uso do servico: a da config ou a padrao do cais.
func Version(c config.Config, name string) string {
	if v := c.Versions[name]; v != "" {
		return v
	}
	return Images[name].Default
}

// ImageRef e a imagem com a tag em uso (o que vai no compose).
func ImageRef(c config.Config, name string) string {
	return Images[name].Repo + ":" + Version(c, name)
}

// TagOf e a tag de uma imagem do servico (ex: do docker compose ps); vazio
// se a imagem e outra.
func TagOf(name, image string) string {
	repo, tag, ok := strings.Cut(image, ":")
	if !ok || repo != Images[name].Repo && repo != "docker.io/"+Images[name].Repo && repo != "docker.io/library/"+Images[name].Repo {
		return ""
	}
	return tag
}

// CheckVersions confere as versoes da config: so dos servicos com versao e
// no formato da imagem (a versao no comeco: e por ela que a troca e
// conferida).
func CheckVersions(c config.Config) error {
	for _, name := range slices.Sorted(maps.Keys(c.Versions)) {
		img, ok := Images[name]
		switch {
		case !ok:
			return fmt.Errorf("versions: %q nao tem versao escolhida (so %s)", name, strings.Join(slices.Sorted(maps.Keys(Images)), ", "))
		case c.Versions[name] != "" && !img.format.MatchString(c.Versions[name]):
			return fmt.Errorf("versao do %s: %q nao e uma tag de %s com a versao (ex: %s)", name, c.Versions[name], img.Repo, img.Example)
		}
	}
	return nil
}

// CheckUpgrade diz se o servico pode trocar da tag from (a que esta no ar,
// com os dados dela no volume) pra to. Os dados nao voltam: versao mais
// velha nunca; o Postgres nao abre os dados de outra versao major (precisa
// de pg_upgrade, ou dump e restore); o Garage muda o formato dos metadados
// entre versoes major (garage migrate). from vazio (nunca subiu) aceita.
func CheckUpgrade(name, from, to string) error {
	if from == "" || from == to {
		return nil
	}
	img := Images[name]
	a, b := parseVersion(img, from), parseVersion(img, to)
	if a == nil || b == nil {
		return nil // tag de fora do formato no ar: sem como comparar
	}
	switch {
	case name == "postgres" && a[0] != b[0]:
		return fmt.Errorf("o Postgres %d nao abre os dados do Postgres %d que estao no volume: troca de versao major precisa de pg_upgrade "+
			"(ou dump de cada banco, volume novo e restore). Aqui so dentro da %d (ex: outro PostGIS ou a imagem nova da mesma versao)", b[0], a[0], a[0])
	case name == "garage" && a[0] != b[0]:
		return fmt.Errorf("o Garage v%d muda o formato dos metadados da v%d (precisa do garage migrate, pela documentacao da versao nova): "+
			"aqui so dentro da v%d", b[0], a[0], a[0])
	case slices.Compare(b, a) < 0:
		return fmt.Errorf("o %s nao volta pra uma versao mais velha (%s, no ar %s): os dados no volume ja estao no formato da versao nova", name, to, from)
	}
	return nil
}

// parseVersion le os numeros da versao no comeco da tag (postgres: so o
// major, o resto e o PostGIS; os outros: major.minor.patch).
func parseVersion(img Image, tag string) []int {
	m := img.format.FindStringSubmatch(tag)
	if m == nil {
		return nil
	}
	var out []int
	for i, s := range m[1:] {
		s = strings.TrimPrefix(s, ".")
		if img.Repo == Images["postgres"].Repo && i > 0 {
			break
		}
		if s == "" {
			s = "0"
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			break // o sufixo (-alpine, -oracle)
		}
		out = append(out, n)
	}
	return out
}
