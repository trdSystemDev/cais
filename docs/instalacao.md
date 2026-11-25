# Instalacao

Como instalar, atualizar e remover o cais numa VPS. O caminho rapido esta no [README](../README.md#instalar).

A VPS precisa de Linux (amd64 ou arm64) com systemd. Sem Docker no Ubuntu
ou no Debian, o `install.sh` instala o Docker e o Compose pelo repositorio
oficial do Docker; em outra distro, instale antes. O `install.sh` baixa o
binario do ultimo release do GitHub: sem clone, sem Go e sem compilar nada na
VPS.

```sh
curl -fsSL https://github.com/trdSystemDev/cais/releases/latest/download/install.sh |
  sudo sh -s -- --ssh
```

O mesmo comando atualiza: baixa o release mais novo, confere o SHA-256 e
reinicia so o daemon (os containers ficam). Na mesma versao, nao faz nada.
`--version v0.3.0` instala uma versao especifica (inclusive mais velha, pra
voltar).

Depois de instalado, `cais update` faz o mesmo sem o curl e sem sudo
(`ssh cais@vps update` do seu computador): o daemon roda o `install.sh` do
release numa unidade do systemd (`cais-update`, log em
`sudo journalctl -u cais-update`), com as opcoes que ja estao no lugar, e o
comando espera ele voltar na versao nova. `cais update -version v0.3.0` vai
pra uma versao especifica. Fora do MCP: a IA nao atualiza o cais.

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
- `--harden NOME`: VPS nova. No fim, roda o `cais host harden -user NOME`
  (admin com sudo e chave, SSH sem root e sem senha, firewall, fail2ban,
  atualizacoes); as perguntas vao pro terminal. Ver
  [Seguranca da VPS](seguranca.md).
- `--dry-run`: mostra o que faria, sem mudar nada.
- `--version vX.Y.Z`: pelo release, essa versao em vez da mais nova.
- `--uninstall`: remove o binario, o servico e o usuario `cais` do ssh;
  `/etc/cais`, `/var/lib/cais` e os containers ficam.

Depois da instalacao, numa VPS nova: `cais host harden -user deploy`
([Seguranca da VPS](seguranca.md)). Em seguida, o [Setup](setup.md).

---

[Voltar pra documentacao](README.md)
