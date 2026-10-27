// Package bootstrap e o Setup da VPS: monta o plano (o que falta e o que vai
// mudar, sem gravar nada) e executa os passos em ordem -- checa o docker,
// gera os secrets, grava os compose das stacks, sobe cada uma esperando os
// healthchecks e faz o login da VPS no registry. Rodar de novo e seguro.
package bootstrap
