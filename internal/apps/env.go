package apps

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv le o formato .env que o proprio cais grava (FormatEnv): uma
// variavel por linha, valor entre aspas simples (literal) ou duplas.
func ParseEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	// \r\n (Windows) e \r sozinho (o colar de varios terminais) viram \n
	data = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "export VAR=valor" (.env escrito pra shell) vale igual
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !envName.MatchString(k) {
			return nil, fmt.Errorf("linha %d invalida no env", n)
		}
		switch {
		case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
			v = v[1 : len(v)-1]
		case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
			v = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(v[1 : len(v)-1])
		}
		env[k] = v
	}
	return env, sc.Err()
}

// FormatEnv grava as variaveis no formato .env do compose. Aspas simples
// deixam o valor literal (sem interpolacao de $); valor com aspa simples vai
// entre duplas, e ai nao pode ter $ (o compose interpolaria).
func FormatEnv(env map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteString("# gerado pelo cais a partir das variaveis do app -- nao edite\n")
	for _, k := range keys {
		v := env[k]
		if !envName.MatchString(k) {
			return nil, fmt.Errorf("variavel invalida %q", k)
		}
		if strings.ContainsAny(v, "\n\r") {
			return nil, fmt.Errorf("%s: valor com quebra de linha (use \\n no texto, ou um secret em arquivo)", k)
		}
		if !strings.Contains(v, "'") {
			fmt.Fprintf(&b, "%s='%s'\n", k, v)
			continue
		}
		if strings.Contains(v, "$") {
			return nil, fmt.Errorf("%s: valor com aspa simples e $ juntos nao cabe no .env; use um secret em arquivo", k)
		}
		fmt.Fprintf(&b, "%s=\"%s\"\n", k, strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v))
	}
	return b.Bytes(), nil
}
