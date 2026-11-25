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

## Varias VPS num MCP so

Com mais de uma VPS, o cais do SEU computador junta todas num MCP so. A lista
fica em `~/.config/cais/remotes.yaml` (so o seu usuario le):

```sh
cais remote add prod cais@prod.tailnet.ts.net     # confere na hora (ssh ... cais version)
cais remote add dev  cais@dev.tailnet.ts.net
cais remote list
claude mcp add -s user cais -- cais mcp --remotes
```

- Toda ferramenta ganha o argumento `vps` (obrigatorio): sem ele, ou com uma
  VPS que nao existe, a chamada e recusada e a resposta lista as VPS. Assim a
  IA nao faz um deploy na VPS errada sem perceber.
- `vps_list`: cada VPS com a versao do cais e o resumo da seguranca, as com
  problema primeiro; uma fora do ar aparece com o motivo do ssh, sem segurar
  as outras.
- `security_check` sem `vps` confere todas ao mesmo tempo.
- Por baixo, cada chamada vai pro `ssh <destino> cais mcp` daquela VPS (aberto
  na primeira chamada e reaproveitado): nada novo abre na VPS e as regras sao
  as dela (nada que apaga dados, seguranca so leitura).
- O ssh do MCP nao pergunta nada: a chave no ssh-agent (ou sem senha) e a
  chave do host ja aceita. O `cais remote add` avisa quando nao entra assim.

A mesma lista serve no terminal: `cais -r prod` abre a TUI da prod, e
`cais -r dev update` (ou qualquer comando) roda na dev.

Sem o cais no computador, tambem da um MCP por VPS, com um nome pra cada (as
ferramentas se repetem por VPS):

```sh
claude mcp add -s user cais-prod -- ssh cais@vps-producao cais mcp
claude mcp add -s user cais-homolog -- ssh cais@vps-homologacao cais mcp
```

As ferramentas chamam as mesmas acoes da TUI e do CLI, com as mesmas regras
(o deploy recusa compose fora do formato e diz a correcao):

- ler: `status`, `security_check`, `infra_status`, `infra_logs`, `apps_list`,
  `app_logs`, `app_guide`, `app_env_get` (so os nomes),
  `databases_list`, `buckets_list`, `app_promote_check`;
- agir: `app_create`, `app_update`, `app_deploy`, `app_rollback`, `app_promote`, `app_restart`, `app_stop`,
  `app_env_set` (senhas inclusive), `database_create`, `bucket_create`,
  `app_link_database`/`app_unlink_database`, `app_link_bucket`/`app_unlink_bucket`.

**"Esta tudo certo com a VPS?"**: o `security_check` roda a checagem de
seguranca na hora (a mesma do Checar de novo da TUI) e devolve so ela: o
resumo, os itens com problema primeiro (cada um com o conserto), o Tailscale
(conectado, nome, IP, vencimento da chave) e se ha versao nova do cais. So
le: corrigir, Preparar VPS, fechar o SSH, reiniciar e atualizar ficam na TUI
(Setup > Seguranca). O link de login do Tailscale nunca sai pelo MCP (quem o
abre poe a VPS na propria tailnet).

Nada que apaga dados nem o Setup: excluir app, banco ou bucket, ligar ou
desligar servico e a config da VPS ficam so na TUI e no CLI. Valores de
variavel nunca sao devolvidos (a IA ve so os nomes): a IA pode gravar uma
senha, mas nao ler.

---

[Voltar pra documentacao](README.md)
