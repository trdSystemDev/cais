// Package compose valida o docker-compose.yml de um app antes do deploy
// (Validate). Substitui o filtro de texto do antigo deploy.sh por uma lista
// de permitidos sobre o YAML parseado: qualquer chave nao avaliada e
// recusada, inclusive opcoes que surgirem em versoes novas do compose.
//
// Protege o host e as outras stacks: nada de modo privilegiado, namespace
// do host, dispositivo, caminho do host fora da pasta do app (volumes,
// env_file, secrets), volume/rede/projeto de outra stack, rede que chega
// no docker.sock, interpolacao com o ambiente do daemon, build/extends/
// include, porta publicada no host ou label do Traefik (o roteamento e do
// cais).
package compose
