# Instalacao

Como instalar, atualizar e remover o cais numa VPS. O caminho rapido esta no [README](../README.md#instalar).

A VPS precisa de Linux (amd64 ou arm64) com systemd e Docker com o Compose
v2. O `install.sh` baixa o binario do ultimo release do GitHub: sem clone,
sem Go e sem compilar nada na VPS.

Enquanto o repositorio for privado, o download precisa de um token do GitHub
so de leitura: em github.com/settings/personal-access-tokens, um
*fine-grained token* com acesso so ao repositorio `trdSystemDev/cais` e a
permissao **Contents: read**. Ele so e usado na hora e nao fica na VPS.

```sh
read -s GH_TOKEN && export GH_TOKEN      # cola o token (sem eco, fora do historico)
curl -fsSL -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github.raw" \
  https://api.github.com/repos/trdSystemDev/cais/contents/install.sh |
  sudo --preserve-env=GH_TOKEN sh -s -- --ssh
```

O mesmo comando atualiza: baixa o release mais novo, confere o SHA-256 e
reinicia so o daemon (os containers ficam). Na mesma versao, nao faz nada.
`--version v0.3.0` instala uma versao especifica (inclusive mais velha, pra
voltar).

Instala `/usr/local/bin/cais` e o servico `cais` (a unidade do systemd vai
dentro do `install.sh`). Opcoes (passe depois do `sh -s --`):

- `--group cais`: libera o cais pra quem esta no grupo, sem sudo (cria o grupo
  e coloca nele quem rodou o sudo). Quem esta no grupo controla a VPS, como no
  grupo `docker`.
- `--ssh`: cria o usuario `cais` pro ssh, com as chaves de quem rodou o sudo
  (ou `--ssh-key arquivo.pub`). `ssh cais@vps` abre direto a TUI,
  `ssh cais@vps apps` roda um comando do cais e `ssh cais@vps cais mcp` e o
  MCP. Sem shell, sem tunel e so por chave (a config fica em
  `/etc/ssh/sshd_config.d/cais.conf`, conferida com `sshd -T` pra nao mudar o
  ssh dos outros usuarios). Implica `--group cais`.
- `--dry-run`: mostra o que faria, sem mudar nada.
- `--version vX.Y.Z`: pelo release, essa versao em vez da mais nova.
- `--uninstall`: remove o binario, o servico e o usuario `cais` do ssh;
  `/etc/cais`, `/var/lib/cais` e os containers ficam.

Depois da instalacao, o [Setup](setup.md).

---

[Voltar pra documentacao](README.md)
