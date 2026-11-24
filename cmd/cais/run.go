package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/trdSystemDev/cais/internal/apps"
)

// execve troca o processo pelo comando (os testes trocam por um gravador).
var execve = syscall.Exec

// exitError e um erro com codigo de saida proprio (127: comando nao
// existe; 126: nao executa), como o shell faz.
type exitError struct {
	code int
	err  error
}

func (x *exitError) Error() string { return x.err.Error() }
func (x *exitError) Unwrap() error { return x.err }

// runRun e o cais run, dentro do container do app: le as variaveis que o
// deploy gravou (o run.env montado em /run/cais/env), junta ao ambiente e
// vira o comando do app (exec: os sinais chegam direto nele, sem processo
// no meio). As variaveis nao vao pra config do container (docker inspect).
// Nao fala com o daemon nem le config: roda em qualquer imagem, ate sem
// shell.
func (e *cli) runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(e.out)
	fs.Usage = func() { fmt.Fprint(e.out, helpFor("run")) }
	envFile := fs.String("env", apps.RunEnvTarget, "arquivo com as variaveis (gravado pelo deploy)")
	if err := e.parse(fs, args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return &exitError{127, errors.New("run: falta o comando do app depois do --, ex: cais run -- node server.js")}
	}
	data, err := os.ReadFile(*envFile)
	if errors.Is(err, os.ErrNotExist) {
		return &exitError{126, fmt.Errorf("run: %s nao existe: monte as variaveis do app no compose (volumes: - ${CAIS_ENV}:%s:ro)", *envFile, apps.RunEnvTarget)}
	}
	if err != nil {
		return &exitError{126, fmt.Errorf("run: %w", err)}
	}
	env := map[string]string{}
	if err := json.Unmarshal(data, &env); err != nil {
		return &exitError{126, fmt.Errorf("run: %s ilegivel: %w", *envFile, err)}
	}
	// a variavel do app vale mais que a da imagem; o PATH novo (se o app
	// tiver um) ja vale pra achar o comando
	for k, v := range env {
		if err := os.Setenv(k, v); err != nil {
			return &exitError{126, fmt.Errorf("run: variavel %s: %w", k, err)}
		}
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		return &exitError{127, fmt.Errorf("run: %w", err)}
	}
	if err := execve(path, argv, os.Environ()); err != nil {
		return &exitError{126, fmt.Errorf("run: %s: %w", path, err)}
	}
	return nil
}
