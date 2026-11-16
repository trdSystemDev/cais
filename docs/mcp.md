# MCP (deploy por IA)

Usar o cais pelo Claude Code (ou outro cliente MCP).

`cais mcp` expoe o cais como servidor MCP pelo stdin/stdout. Ele roda na VPS e
o cliente MCP no seu computador chama pelo SSH: a autenticacao e a sua chave
SSH e nenhuma porta nova abre.

```sh
claude mcp add -s user cais -- ssh cais@vps cais mcp     # instalado com --ssh
claude mcp add -s user cais -- ssh root@vps cais mcp     # sem o --ssh
```

`-s user` deixa o cais disponivel em todos os projetos do Claude Code (sem
ele, so no projeto atual). O ssh do MCP nao pergunta nada: entre uma vez com
`ssh cais@vps` antes, pra aceitar a chave do servidor.

Com mais de uma VPS, um nome pra cada:

```sh
claude mcp add -s user cais-prod -- ssh cais@vps-producao cais mcp
claude mcp add -s user cais-homolog -- ssh cais@vps-homologacao cais mcp
```

As ferramentas chamam as mesmas acoes da TUI e do CLI, com as mesmas regras
(o deploy recusa compose fora do formato e diz a correcao):

- ler: `status`, `infra_status`, `infra_logs`, `apps_list`,
  `app_logs`, `app_guide`, `app_env_get`, `app_secrets_list`,
  `databases_list`, `buckets_list`, `app_promote_check`;
- agir: `app_create`, `app_update`, `app_deploy`, `app_rollback`, `app_promote`, `app_restart`, `app_stop`,
  `app_env_set`, `app_secret_set`, `database_create`, `bucket_create`,
  `app_link_database`/`app_unlink_database`, `app_link_bucket`/`app_unlink_bucket`.

Nada que apaga dados nem o Setup: excluir app, banco, bucket ou secret, ligar
ou desligar servico e a config da VPS ficam so na TUI e no CLI. Valores de secret nunca sao
devolvidos (a IA ve so os nomes).

---

[Voltar pra documentacao](README.md)
