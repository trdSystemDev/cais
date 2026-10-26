# vpsctl

Painel TUI (Go + tview, com suporte a mouse) para preparar e administrar uma VPS
que so tem Docker instalado: BunkerWeb, registry, bancos Postgres/MySQL,
secrets por app e CI com Woodpecker.

Roda direto no host, pela sessao SSH: nao abre porta e nao tem login proprio.

## Estrutura

| Caminho | Responsabilidade |
|---|---|
| `cmd/vpsctl` | ponto de entrada |
| `internal/ui` | paginas tview clicaveis |
| `internal/bootstrap` | assistente da VPS vazia |
| `internal/stacks` | stacks base + `templates/` embutidos |
| `internal/docker` | SDK do Docker + `docker compose` |
| `internal/compose` | parse e validacao (regras do `deploy.sh`) |
| `internal/secrets` | secrets centrais e por app |
| `internal/databases` | databases/usuarios Postgres e MySQL |
| `internal/apps` | apps em `/deploy/apps/<app>` |
| `internal/github` | link do GitHub, deteccao de CI |
| `internal/woodpecker` | cliente da API do Woodpecker |
| `internal/config` | `/etc/vpsctl/config.yaml` |
| `assets/` | `deploy.sh` (forced command) e CI de exemplo |

Os arquivos em `internal/stacks/templates/` sao copia dos compose do repo `VPS`
e ainda serao parametrizados (dominio, e-mail etc.) na fase 2.

## Roadmap

1. Esqueleto: layout tview com abas clicaveis, checagem de Docker/Compose
2. Bootstrap: templates das stacks + assistente da VPS vazia
3. Apps e secrets: logs ao vivo, restart, propagar secret
4. Bancos
5. GitHub + Woodpecker: criar app pelo link, detectar/gerar CI
6. Backups, releases e `install.sh`

## Build

```sh
make build   # gera dist/vpsctl-linux-amd64 e dist/vpsctl-linux-arm64
```
