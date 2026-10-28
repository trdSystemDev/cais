// Package bootstrap e o Setup da VPS: monta o plano (o que falta e o que vai
// mudar, sem gravar nada) e executa os passos em ordem -- checa o docker,
// gera os secrets, grava os compose das stacks e sobe cada uma esperando os
// healthchecks. Rodar de novo e seguro.
package bootstrap
