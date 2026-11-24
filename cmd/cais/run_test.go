package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// execRec troca o exec do cais run por um gravador.
func execRec(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := execve
	execve = func(path string, argv, env []string) error {
		got = append([]string{path}, argv...)
		got = append(got, env...)
		return nil
	}
	t.Cleanup(func() { execve = old })
	return &got
}

func writeRunEnv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte(content), 0o444); err != nil {
		t.Fatal(err)
	}
	return p
}

// o cais run junta as variaveis ao ambiente (qualquer valor, e a do app
// vale mais que a da imagem) e vira o comando, sem falar com o daemon
func TestRunInjetaEExecuta(t *testing.T) {
	got := execRec(t)
	// o run grava no ambiente deste processo: t.Setenv devolve como estava
	for _, k := range []string{"CHAVE", "ASPAS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("DA_IMAGEM", "imagem")
	t.Setenv("TROCADA", "imagem")
	env := writeRunEnv(t, `{"CHAVE":"-----BEGIN-----\nabc\n-----END-----","ASPAS":"a'b \"c\" $x","TROCADA":"app"}`)

	tc := newTestCLI(nil) // sem daemon: o run nao pode precisar dele
	if code := tc.run([]string{"run", "-env", env, "--", "sh", "-c", "echo oi"}); code != 0 {
		t.Fatalf("saida %d: %s", code, tc.errb)
	}
	sh, _ := exec.LookPath("sh")
	if len(*got) < 4 || !slices.Equal((*got)[:4], []string{sh, "sh", "-c", "echo oi"}) {
		t.Fatalf("exec %q", *got)
	}
	for _, want := range []string{"CHAVE=-----BEGIN-----\nabc\n-----END-----", `ASPAS=a'b "c" $x`, "TROCADA=app", "DA_IMAGEM=imagem"} {
		if !slices.Contains(*got, want) {
			t.Errorf("ambiente sem %q", want)
		}
	}
}

func TestRunErros(t *testing.T) {
	execRec(t)
	env := writeRunEnv(t, `{}`)
	cases := map[string]struct {
		args []string
		code int
		want string
	}{
		"sem comando":         {[]string{"run", "-env", env, "--"}, 127, "falta o comando"},
		"comando nao existe":  {[]string{"run", "-env", env, "--", "comando-que-nao-existe-xyz"}, 127, "executable file not found"},
		"sem o arquivo":       {[]string{"run", "-env", filepath.Join(t.TempDir(), "nao"), "--", "sh"}, 126, "${CAIS_ENV}:/run/cais/env:ro"},
		"arquivo ilegivel":    {[]string{"run", "-env", writeRunEnv(t, "A=1"), "--", "sh"}, 126, "ilegivel"},
		"flag que nao existe": {[]string{"run", "-x", "--", "sh"}, 2, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tc := newTestCLI(nil)
			if code := tc.run(c.args); code != c.code {
				t.Fatalf("saida %d, esperado %d: %s", code, c.code, tc.errb)
			}
			if !strings.Contains(tc.errb.String(), c.want) {
				t.Errorf("stderr sem %q: %s", c.want, tc.errb)
			}
		})
	}
}

// de verdade: o processo vira o comando (exec) e o comando ve a variavel
func TestRunExecDeVerdade(t *testing.T) {
	if os.Getenv("CAIS_RUN_HELPER") == "1" {
		e := &cli{out: os.Stdout, err: os.Stderr, in: os.Stdin}
		os.Exit(e.run([]string{"run", "-env", os.Getenv("CAIS_RUN_ENV"), "--", "sh", "-c", `printf %s "$SEGREDO"`}))
	}
	env := writeRunEnv(t, `{"SEGREDO":"valor com espaco e $"}`)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunExecDeVerdade$")
	cmd.Env = append(os.Environ(), "CAIS_RUN_HELPER=1", "CAIS_RUN_ENV="+env)
	out, err := cmd.Output()
	if err != nil || string(out) != "valor com espaco e $" {
		t.Fatalf("saida %q %v", out, err)
	}
}
