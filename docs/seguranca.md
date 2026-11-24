# Seguranca da VPS

O `cais host` prepara uma VPS nova sem as falhas de sempre (root entrando por
senha, nenhum firewall, porta de banco aberta) e depois fica conferindo. Roda
na propria VPS, como root, e nao depende do daemon.

| Comando | O que faz |
|---|---|
| `cais host check` | so le: diz o que esta certo, o que falta e como corrigir |
| `cais host harden -user NOME` | prepara a VPS (abaixo) |
| `cais host confirm` | do login novo do admin: mantem o SSH do harden |

A mesma checagem aparece na TUI, em **Setup > Seguranca**, e no estado que o
MCP le. O daemon refaz a checagem de hora em hora.

## Pela TUI

Em **Setup > Seguranca**:

- Cada item com problema mostra o conserto. Firewall, fail2ban, atualizacoes
  automaticas e logs dos containers tem o botao **Corrigir**: corrige so
  aquele item, instalando o pacote se faltar, sem mexer no SSH nem nos
  usuarios. Serve pra VPS que ja esta em uso.
- **Preparar VPS** e o `cais host harden` pela TUI. Voce preenche o nome do
  admin e a senha do sudo (duas vezes; vazia se o admin ja existe com senha),
  clica em **Aplicar** e os passos aparecem ao vivo. Nao precisa da senha do
  root: o daemon ja roda como root, e quem abre a TUI ja passou pela chave
  SSH ou pelo sudo.
- Quando o SSH muda, aparece a contagem regressiva. Abra **outra** janela do
  terminal e entre de novo (`ssh cais@<ip-da-vps>` ou `ssh deploy@<ip-da-vps>`).
  O botao **Confirmar** so aparece na janela nova, porque so uma sessao aberta
  depois da mudanca prova que a chave funciona. Sem confirmar, o SSH volta
  sozinho em 5 minutos.

Nada disso esta no MCP: a IA le a checagem, mas nao mexe no SSH nem no
firewall.

## VPS nova (Hostinger)

1. Crie a VPS com **Ubuntu 24.04** (ou outro Ubuntu LTS, ou Debian 12) e
   cadastre a sua chave SSH no painel. Se esquecer a chave, do seu computador:
   `ssh-copy-id root@<ip-da-vps>`.
2. Entre como root, instale o cais e prepare a maquina num comando so. Sem
   Docker, o `install.sh` instala o Docker pelo repositorio oficial; o
   `--harden deploy` roda o `cais host harden -user deploy` no fim:

   ```sh
   ssh root@<ip-da-vps>
   curl -fsSL https://github.com/trdSystemDev/cais/releases/latest/download/install.sh | sh -s -- --ssh --harden deploy
   ```

3. O harden mostra o plano, pede `sim` e a senha do sudo do `deploy` (o SSH
   continua so por chave; a senha e so pro `sudo`). Sem o `--harden`, rode
   depois `cais host harden -user deploy`, ou use o **Preparar VPS** da TUI.
4. **Sem fechar esse terminal**, abra outro no seu computador:

   ```sh
   ssh deploy@<ip-da-vps>
   sudo cais host confirm
   ```

   Sem o `confirm` em 5 minutos, a mudanca no SSH volta sozinha: o root e a
   senha voltam a entrar e voce nao fica trancado fora se a chave nao
   funcionar. Depois e so rodar o harden de novo.
5. `sudo cais host check` deve mostrar tudo `[ok]`. Se mostrar "Reinicio
   pendente", reinicie quando puder (`sudo reboot`).

Dali pra frente, entre como `deploy` (com `sudo`) ou pelo `ssh cais@vps`
(TUI). O root nao entra mais pelo SSH; o console do painel da Hostinger
continua funcionando como acesso de emergencia.

## O que o harden faz

Cada passo confere antes e so muda o que falta: rodar de novo e seguro.

1. **Sistema atualizado**: `apt-get update` e `full-upgrade`; instala `sudo`,
   `ufw`, `fail2ban` e `unattended-upgrades`.
2. **Atualizacoes de seguranca automaticas**
   (`/etc/apt/apt.conf.d/20auto-upgrades`). Sao so as de seguranca e a VPS nao
   reinicia sozinha: o `cais host check` avisa quando um reinicio fica
   pendente.
3. **Logs dos containers com limite** (`/etc/docker/daemon.json`, driver
   `local`, 10 MB x 3 arquivos). As outras chaves do arquivo ficam. Vale pros
   containers criados depois (o proximo deploy); o Docker nao e reiniciado.
4. **Usuario admin**: criado se faltar, no grupo `sudo`, com as chaves SSH do
   root (`/root/.ssh/authorized_keys`) e as do `-key arquivo.pub`. Sem
   nenhuma chave o harden recusa, porque desligar a senha trancaria todo mundo.
5. **Firewall (ufw)**: bloqueia a entrada por padrao e libera so 22 (SSH), 80
   e 443 (o proxy; 443/udp e o HTTP/3). O 22 e liberado antes de ligar. Regras
   antigas ficam, e o `check` aponta as que sobram.
6. **fail2ban** no SSH: 5 erros em 10 minutos bloqueiam o IP por 1 hora
   (`/etc/fail2ban/jail.d/cais.conf`, lendo o journal).
7. **SSH so com chave e sem root**, em
   `/etc/ssh/sshd_config.d/00-cais-harden.conf`: `PermitRootLogin no`,
   `PasswordAuthentication no`, `KbdInteractiveAuthentication no`. O `00-`
   vem antes do `50-cloud-init.conf` das imagens de nuvem, que liga a senha
   (no sshd vale o primeiro valor). Antes de recarregar, o harden:
   - valida a config com `sshd -t`;
   - confere com `sshd -T` que ela vale de fato e que o admin entra por chave;
   - agenda a volta automatica (`systemd-run`, unidade `cais-ssh-rollback`).

   Qualquer erro desfaz o arquivo antes de recarregar.

A porta do SSH continua a 22. Com chave e fail2ban, trocar a porta so diminui
o barulho no log.

## Portas e o Docker

O Docker publica portas por fora do ufw: um `ports: "5432:5432"` fica aberto
pra internet mesmo com o firewall ligado. Por isso:

- o deploy do cais recusa compose com porta publicada ([apps](apps.md)); o
  acesso de fora e pelo proxy (dominio);
- os bancos da infra escutam so no `127.0.0.1`, e o acesso e por tunel SSH
  ([setup](setup.md#acesso-aos-bancos));
- o `cais host check` lista toda porta aberta pra fora alem da 22, 80 e 443,
  inclusive a de container criado fora do cais, com o nome dele.

Pra publicar algo so pra VPS, use `"127.0.0.1:5000:80"`.

## Firewall do painel da Hostinger

A Hostinger tem um firewall proprio no painel da VPS, na frente da maquina.
E opcional e soma com o ufw: se usar, libere as mesmas portas (22, 80 e
443 TCP, 443 UDP). Nao precisa duplicar nada mais.

## Trancado fora

- **Antes do confirm**: espere 5 minutos, o SSH volta sozinho.
- **Depois**: entre pelo console do painel da Hostinger (VPS > Terminal do
  navegador), como root, e:
  - apague `/etc/ssh/sshd_config.d/00-cais-harden.conf`;
  - rode `systemctl reload ssh`;
  - acerte a chave em `/home/<admin>/.ssh/authorized_keys`;
  - rode o harden de novo.

---

[Voltar pra documentacao](README.md)
