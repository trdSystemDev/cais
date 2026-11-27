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
  automaticas, logs dos containers e a chave do `ssh cais@vps` tem o botao
  **Corrigir**: corrige so aquele item, instalando o pacote se faltar, sem
  mexer na config do SSH nem no admin. Serve pra VPS que ja esta em uso.
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
   pendente", reinicie quando puder ou agende (veja [Reinicio](#reinicio)).

Dali pra frente, entre como `deploy` (com `sudo`) ou pelo `ssh cais@vps`
(TUI). O root nao entra mais pelo SSH; o console do painel da Hostinger
continua funcionando como acesso de emergencia.

## O que o harden faz

Cada passo confere antes e so muda o que falta: rodar de novo e seguro.

1. **Docker** (`/etc/docker/daemon.json`; as outras chaves do arquivo
   ficam), antes da atualizacao do sistema:
   - **logs dos containers com limite** (driver `local`, 10 MB x 3
     arquivos). Vale pros containers criados depois (o proximo deploy).
   - **`live-restore`**: os containers continuam rodando quando o Docker
     reinicia. A atualizacao do `docker-ce` e do `containerd.io` (no passo
     seguinte, ou no Corrigir dos Pacotes do sistema) reinicia o Docker, e
     sem ele todos os containers caem e sobem de novo. Entra pelo
     `systemctl reload docker`, sem parar nada. Num salto grande de versao
     do Docker os containers ainda podem reiniciar; com o swarm ligado ele
     nao vale e fica de fora.
2. **Sistema atualizado**: `apt-get update` e `full-upgrade`; instala `sudo`,
   `ufw`, `fail2ban` e `unattended-upgrades`. A TUI mostra quantos pacotes
   ja foram instalados e avisa quando o Docker esta na lista.
3. **Atualizacoes de seguranca automaticas**
   (`/etc/apt/apt.conf.d/20auto-upgrades`). Sao so as de seguranca e a VPS nao
   reinicia sozinha: o `cais host check` avisa quando um reinicio fica
   pendente (e o [horario automatico](#reinicio) reinicia de madrugada).
4. **Usuario admin**: criado se faltar, no grupo `sudo`, com as chaves SSH do
   root (`/root/.ssh/authorized_keys`) e as do `-key arquivo.pub`. Sem
   nenhuma chave o harden recusa, porque desligar a senha trancaria todo mundo.
5. **Chaves do usuario `cais`** (o `ssh cais@vps` da TUI, se o `install.sh`
   rodou com `--ssh`): recebe as chaves do root, as dos admins do `sudo` e as
   do `-key`. O instalador so copia as que o root tem na hora; quem instalou
   entrando por senha e so depois fez o `ssh-copy-id` ficava com o `cais` sem
   chave (`Permission denied (publickey)`). O `check` avisa e o **Corrigir**
   copia (abra a TUI com `sudo cais` pelo admin).
6. **Firewall (ufw)**: bloqueia a entrada por padrao e libera so 22 (SSH), 80
   e 443 (o proxy; 443/udp e o HTTP/3). O 22 e liberado antes de ligar. Regras
   antigas ficam, e o `check` aponta as que sobram.
7. **fail2ban** no SSH: 5 erros em 10 minutos bloqueiam o IP por 1 hora
   (`/etc/fail2ban/jail.d/cais.conf`, lendo o journal).
8. **SSH so com chave e sem root**, em
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

## Pacotes do sistema: uma vez por mes

As atualizacoes de seguranca chegam sozinhas, todo dia (unattended-upgrades).
O resto (pacotes comuns e o Docker) fica pra voce, uma vez por mes. O item
**Pacotes do sistema** da checagem conta o que o `full-upgrade` atualizaria
agora (pela simulacao, `apt-get -s`: os phased updates do Ubuntu, liberados
aos poucos, nao contam) e le no historico do apt (`/var/log/apt/history.log`,
e o do mes anterior) a ultima atualizacao feita a mao; as do
unattended-upgrades nao contam. Com pacote esperando e a ultima ha mais de 30
dias, o item pede a atualizacao.

O **Corrigir** (dois cliques) roda `apt-get update` e `full-upgrade`. Se o
Docker atualizar, os containers reiniciam por alguns segundos; se o kernel
atualizar, aparece o **Reinicio pendente**. A mao:
`sudo apt update && sudo apt full-upgrade`.

## Reinicio

Atualizacao de kernel ou biblioteca so vale depois de reiniciar, e com
varios apps na VPS reiniciar no meio do dia derruba todos por 1 a 2
minutos. Na TUI, em **Setup > Seguranca > Reinicio**, com o **Horario**
(HH:MM, no fuso da VPS):

- **Agendar uma vez**: reinicia na proxima vez desse horario (hoje ou
  amanha). **Cancelar o agendado** desfaz.
- **Sempre neste horario**: o horario automatico (`reboot_time` na
  config). Quando uma atualizacao pedir reinicio, a VPS reinicia sozinha
  na proxima vez desse horario; sem reinicio pendente, nada acontece. O
  daemon confere de hora em hora. **Desligar o automatico** volta ao manual.

O **Corrigir** do item "Reinicio pendente" continua reiniciando na hora.
Com o reinicio agendado, o item fica `[ok]` com a hora.

Pelo terminal, na VPS:

```sh
sudo cais host reboot             # o pendente, o agendado e o automatico
sudo cais host reboot 01:00       # uma vez, na proxima 01:00
sudo cais host reboot cancel      # desfaz o agendado
sudo cais host reboot auto 01:00  # sempre que uma atualizacao pedir
sudo cais host reboot auto off    # desliga o automatico
```

O agendado e um timer do systemd (`cais-reboot-at.timer`) que some no
proprio reinicio. Como no Corrigir, o reinicio espera os deploys e backups
em andamento; os containers e o daemon voltam sozinhos. Evite o mesmo
horario do backup: ele atrasa o reinicio ate terminar.

## Acessos: dar e tirar o SSH de alguem

Com o SSH so por chave, o `ssh-copy-id` nao funciona mais (ele entra por
senha): quem ja e admin cadastra a chave da pessoa. Em **Setup > Acessos**:

- **Chaves**: cada chave publica (pelo comentario, ex: `maria@pc`, e a
  impressao digital) e onde ela entra: `admin <nome>`, `TUI` (o `ssh
  cais@vps`) e `root` (que nao entra pelo SSH depois do harden, mas o harden
  copia as chaves dele). **Remover** (dois apertos) tira a chave de todo
  lugar, senao o proximo harden ou Corrigir a traria de volta.
- **Admins**: os usuarios do grupo `sudo`. **Remover admin** tira do `sudo`,
  expira a conta (o SSH recusa ate com chave) e tira as chaves so dele, dele e
  do `cais`. A pasta `/home/<nome>` fica.
- **Dar acesso**: cole a chave PUBLICA da pessoa (a linha do
  `~/.ssh/id_ed25519.pub`; sem uma: `ssh-keygen -t ed25519`) e escolha:
  - **Dar acesso a TUI**: so o `ssh cais@vps` (deploy, logs, apps; sem shell).
    A TUI controla a VPS inteira, entao e pra quem administra os apps.
  - **Dar acesso de admin**: um usuario proprio com shell e `sudo` (`ssh
    maria@vps`), criado se faltar, com a senha do `sudo` (duas vezes; o SSH
    continua so por chave). A chave entra tambem no `cais`, como a dos outros
    admins. Um usuario por pessoa: nao compartilhe o `deploy`.

O cais recusa tirar a ultima chave de admin (ou do root, se o SSH ainda
deixa ele entrar): todo mundo ficaria trancado fora.

**Com o SSH so pela tailnet**, a chave nao basta: a pessoa precisa da VPS no
Tailscale dela. No painel do Tailscale (login.tailscale.com/admin/machines),
menu **...** da VPS > **Share** (so esta maquina, pra quem e de fora), ou
**Users > Invite** (a tailnet inteira, pra equipe).

Pelo terminal, na VPS:

```sh
sudo cais host access                              # chaves e admins
sudo cais host access add joao.pub                 # so a TUI
sudo cais host access add -admin maria maria.pub   # admin (pede a senha do sudo)
echo 'ssh-ed25519 AAAA... joao@pc' | sudo cais host access add -
sudo cais host access remove SHA256:...            # a chave, de todo lugar
sudo cais host access remove -admin maria          # o admin
```

Fora do MCP: a IA nao da nem tira acesso.

## Tailscale: SSH so pela VPN

Com o Tailscale, o SSH (a TUI, o MCP e o admin) entra so pela sua tailnet, e
da internet ficam abertas so a 80 e a 443 (os sites). Em **Setup >
Seguranca > Tailscale**:

1. **Instalar e conectar** instala o Tailscale pelo repositorio oficial
   (Ubuntu e Debian) e mostra um link, como o Conectar do GitHub: abra no
   navegador (ctrl+clique ou **Copiar link**), entre na sua conta e autorize
   a VPS. Depois, **Ja autorizei**. A VPS entra na tailnet com um IP `100.x`
   e um nome no MagicDNS (`srv.tailXXXX.ts.net`). O DNS da VPS e dos
   containers nao muda (`--accept-dns=false`).
2. Teste do seu computador, com o Tailscale ligado nele:
   `ssh cais@<nome-ou-ip-da-tailnet>`.
3. **Fechar o SSH publico** (dois cliques) libera o 22 so na interface do
   Tailscale (`ufw allow in on tailscale0 to any port 22`), agenda a volta e
   so entao tira a regra publica do 22. Como no harden: sem o **Confirmar** de
   uma sessao aberta depois (que so pode ter vindo pela tailnet) em 5
   minutos, o 22 abre de novo sozinho. Pelo CLI, a confirmacao e o mesmo
   `sudo cais host confirm`.

A checagem ganha dois itens: **Tailscale** (conectado) e, com ele, **SSH so
pela tailnet**. O WireGuard do Tailscale (41641/udp) e o que escuta so nos IPs
da tailnet (100.64.0.0/10) nao contam como porta aberta, e rodar o harden de
novo nao reabre o 22.

Apps so pra quem esta na tailnet (ex: `*.dev.exemplo.com.br` apontando pro
IP `100.x` da VPS) tiram certificado pelo DNS da Cloudflare: ver
[setup](setup.md#certificados-pelo-dns-cloudflare-e-tailscale).

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
- **SSH fechado pra internet e o Tailscale fora do ar**: pelo console do
  painel da Hostinger, como root, `ufw allow 22/tcp` (abre o 22 de novo) e
  conserte o Tailscale (`tailscale up`).
- **Depois**: entre pelo console do painel da Hostinger (VPS > Terminal do
  navegador), como root, e:
  - apague `/etc/ssh/sshd_config.d/00-cais-harden.conf`;
  - rode `systemctl reload ssh`;
  - acerte a chave em `/home/<admin>/.ssh/authorized_keys`;
  - rode o harden de novo.

---

[Voltar pra documentacao](README.md)
