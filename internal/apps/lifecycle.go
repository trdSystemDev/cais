package apps

import (
	"context"
	"fmt"
	"os"

	"github.com/trdSystemDev/cais/internal/docker"
)

// Status lista os containers do app. Antes do primeiro deploy, nenhum.
func (m Manager) Status(ctx context.Context, name string) ([]docker.Container, error) {
	a, err := m.Get(name)
	if err != nil {
		return nil, err
	}
	if !m.deployed(a) {
		return nil, nil
	}
	return m.Docker.PS(ctx, m.project(a, SrcDir))
}

// Restart reinicia um service do app (ou todos, com service vazio).
func (m Manager) Restart(ctx context.Context, name, service string) error {
	a, err := m.requireDeployed(name)
	if err != nil {
		return err
	}
	return m.Docker.Restart(ctx, m.project(a, SrcDir), service)
}

// Stop para o app sem apagar nada; Reiniciar ou o proximo deploy volta.
func (m Manager) Stop(ctx context.Context, name string) error {
	a, err := m.requireDeployed(name)
	if err != nil {
		return err
	}
	return m.Docker.Stop(ctx, m.project(a, SrcDir))
}

// Delete remove o app: containers, redes e imagens dele, e a pasta (codigo,
// variaveis e secrets). Com volumes, apaga tambem os volumes do compose (os
// dados). Database criado na infra nao e do app e fica.
func (m Manager) Delete(ctx context.Context, name string, volumes bool) error {
	if _, err := m.Get(name); err != nil {
		return err
	}
	if err := m.Docker.Down(ctx, name, volumes); err != nil {
		return err
	}
	return os.RemoveAll(m.dir(name))
}

func (m Manager) requireDeployed(name string) (App, error) {
	a, err := m.Get(name)
	if err != nil {
		return a, err
	}
	if !m.deployed(a) {
		return a, fmt.Errorf("app %s ainda nao teve deploy", name)
	}
	return a, nil
}
