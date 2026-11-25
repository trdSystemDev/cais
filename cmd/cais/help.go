package main

import (
	"fmt"
	"strings"

	"github.com/trdSystemDev/cais/internal/databases"
)

// command e um comando do CLI: o resumo vai no cais help e os detalhes no
// -h dele (cais app deploy -h).
type command struct {
	name  string // como o -h procura: "app deploy", "service"
	usage string // linha de uso, sem o "cais "
	short string // resumo; mais de uma linha com \n
	long  string // detalhes, flags e exemplos
}

// section agrupa os comandos no cais help. group e o comando que mostra so
// essa secao (cais app -h); vazio nas secoes sem grupo.
type section struct {
	title string
	group string
	cmds  []command
}

const socketNote = "O socket padrao e /run/cais/cais.sock (ou a variavel CAIS_SOCKET); todo comando aceita --socket."

var sections = []section{
	{title: "Uso:", cmds: []command{
		{name: "tui", usage: "[--socket caminho]", short: "abre a interface (TUI)"},
		{name: "daemon", usage: "daemon [--socket] [--group] [--auto-every 1m] [--deploy-timeout 30m]", short: "roda o daemon (dono do estado e das acoes)", long: `
Normalmente roda pelo systemd (assets/systemd/cais.service). A TUI e o CLI
sao clientes dele pelo socket, que nasce com permissao 0660: so o root, ou o
grupo de --group, usa o cais. Quem acessa o socket controla a VPS.

O daemon tambem faz o deploy automatico dos apps marcados: consulta o
GitHub a cada --auto-every e sobe o commit novo da branch.

Um deploy (download, build, troca) que passa de --deploy-timeout e
cancelado e a versao anterior volta. Ao parar (SIGTERM), o daemon espera os
deploys e backups em andamento; a unidade do systemd espera ate 45min.

Flags:
  --group grupo            grupo que tambem pode usar o cais, alem do root
  --auto-every tempo       intervalo da consulta do deploy automatico (padrao 1m)
  --deploy-timeout tempo   limite de um deploy (padrao 30m; 0 usa o padrao,
                           negativo nao limita)
  --log-format text|json   formato do log no stderr (padrao text: chave=valor)
  --log-level nivel        debug, info, warn ou error (padrao info)

O log tem campos pra filtrar (acao, app, commit, duracao, erro), ex:
journalctl -u cais | grep app=loja`},
		{name: "state", usage: "state [--socket]", short: "imprime o estado atual em JSON"},
		{name: "check", usage: "check [--socket]", short: "checa o ambiente e imprime o resultado em JSON", long: `
Confere Docker e Docker Compose (instalados, versao, daemon no ar).`},
		{name: "mcp", usage: "mcp [--socket] [--remotes]", short: "servidor MCP pelo stdin/stdout (pra IA, via ssh)", long: `
Expoe o cais como ferramentas MCP: ler o estado, logs e a ajuda do compose;
criar e atualizar app, deploy, restart, stop, variaveis (so os nomes voltam,
nunca os valores), criar e ligar banco e bucket. Nada que apaga dados nem o
Setup: excluir app, banco ou bucket, ligar ou desligar servico e a config da
VPS ficam so na TUI e no CLI.

Roda na VPS e conversa pelo stdin/stdout; o cliente MCP no seu computador
chama pelo ssh (a autenticacao e a sua chave SSH, sem porta nova).

Exemplo (Claude Code, no seu computador):
  claude mcp add cais -- ssh cais@vps cais mcp   # instalado com --ssh
  claude mcp add cais -- ssh root@vps cais mcp

Varias VPS num MCP so: com --remotes, roda no SEU computador e fala com as
VPS da lista (cais remote add) pelo ssh de cada uma. Toda ferramenta ganha o
argumento vps; mais o vps_list (versao e seguranca de cada uma) e o
security_check sem vps (todas):
  claude mcp add -s user cais -- cais mcp --remotes`},
		{name: "run", usage: "run [-env arquivo] -- <comando> [args...]", short: "no container do app: injeta as variaveis e roda", long: `
Roda dentro do container do app, como entrypoint: le as variaveis que o
deploy gravou (montadas em /run/cais/env), junta ao ambiente e vira o comando
(exec, os sinais chegam direto no app). Nada vai pra config do container
(docker inspect). Nao fala com o daemon; funciona em qualquer imagem Linux,
ate sem shell. O trecho do compose sai pronto em cais app guide.

Exemplo (no compose do app):
  entrypoint: ["/cais", "run", "--", "node", "server.js"]
  volumes:
    - ${CAIS_BIN}:/cais:ro
    - ${CAIS_ENV}:/run/cais/env:ro

Comando avulso com as variaveis (migracao, console):
  docker compose -p <app> exec web /cais run -- php artisan migrate`},
		{name: "update", usage: "update [-version v0.5.3]", short: "atualiza o cais pro release mais novo (os apps continuam no ar)", long: `
Pede ao daemon pra rodar o install.sh do release, o mesmo comando da
instalacao, com as opcoes que ja estao no lugar (grupo e usuario cais do
ssh). O install.sh confere o SHA-256 do binario e reinicia so o daemon, que
espera deploys e backups em andamento; os apps e os bancos nao param. O
comando espera o daemon voltar na versao nova.

Pelo daemon, nao precisa de sudo: do seu computador, ssh cais@vps update.

Flags:
  -version tag   um release especifico (ex: v0.5.3); serve pra voltar de versao

O log da atualizacao: sudo journalctl -u cais-update`},
		{name: "version", usage: "version", short: "mostra a versao"},
		{name: "help", usage: "help [comando]", short: "esta ajuda; com o comando, os detalhes dele", long: `
Exemplos:
  cais help app            comandos de app
  cais help app deploy     o mesmo que: cais app deploy -h`},
	}},
	{title: "Varias VPS (no seu computador; cada uma pelo ssh):", group: "remote", cmds: []command{
		{name: "remote add", usage: "remote add NOME DESTINO", short: "poe a VPS na lista deste computador (cais -r e o MCP)", long: `
A lista fica em ~/.config/cais/remotes.yaml (ou CAIS_REMOTES), so pro seu
usuario. O mesmo nome troca o destino. Confere na hora: ssh DESTINO cais
version, sem perguntar nada, como o MCP vai entrar (a chave no ssh-agent e a
chave do host ja aceita).

Depois:
  cais -r NOME                 a TUI da VPS
  cais -r NOME update          um comando nela (qualquer um do cais)
  cais mcp --remotes           o MCP de todas (claude mcp add ...)`},
		{name: "remote list", usage: "remote [list]", short: "as VPS da lista"},
		{name: "remote remove", usage: "remote remove NOME", short: "tira a VPS da lista (nada muda nela)"},
	}},
	{title: "Seguranca da VPS (na propria VPS, com sudo; nao usa o daemon):", group: "host", cmds: []command{
		{name: "host check", usage: "host [check]", short: "confere a seguranca da VPS e diz como corrigir", long: `
So le. Confere: sistema (Ubuntu/Debian), login do root e senha no SSH,
firewall (ufw), fail2ban, atualizacoes de seguranca automaticas, portas
abertas pra internet (inclusive as publicadas por containers, que passam
por cima do firewall), limite nos logs dos containers e reinicio pendente.
Sai com 1 se algum item precisa de correcao. O mesmo aparece na TUI, em
Setup > Seguranca, com o Corrigir de cada item e o Preparar VPS.`},
		{name: "host harden", usage: "host harden -user NOME [-key arquivo] [-yes]", short: "prepara uma VPS nova: admin com sudo e so chave, SSH sem\nroot e sem senha, firewall, fail2ban, atualizacoes", long: `
Pra VPS recem-criada (Ubuntu ou Debian), logo depois do install.sh, como
root. Mostra o que vai fazer e pede "sim". Passos (rodar de novo so
completa o que falta):
  1. atualiza o sistema; instala ufw, fail2ban e unattended-upgrades
  2. atualizacoes de seguranca automaticas, sem reiniciar sozinho
  3. limite nos logs dos containers (/etc/docker/daemon.json)
  4. o admin NOME: criado se faltar, no grupo sudo, com as chaves SSH do
     root (e as do -key); pede a senha do sudo dele
  5. firewall: entram so 22 (SSH), 80 e 443 (proxy)
  6. fail2ban no SSH (5 erros em 10 min bloqueiam o IP por 1 hora)
  7. SSH sem login do root e sem senha, so chave

Sem nenhuma chave SSH recusa: desligar a senha trancaria voce fora. Depois
do passo 7, entre de outro terminal com ssh NOME@vps e rode
"sudo cais host confirm" em ate 5 minutos; sem isso o SSH volta sozinho
a aceitar o root e a senha.

Flags:
  -user NOME     o admin que substitui o root no SSH (ex: deploy)
  -key arquivo   chave publica a mais pro admin (.pub ou authorized_keys)
  -yes           nao pede confirmacao (a senha do sudo ainda e pedida)

O mesmo pela TUI: Setup > Seguranca > Preparar VPS. Ou ja na instalacao:
  curl -fsSL <link do install.sh no README> | sh -s -- --ssh --harden deploy`},
		{name: "host confirm", usage: "host confirm", short: "do login novo do admin: mantem o SSH do harden (senao\nvolta sozinho em 5 min)", long: `
Rode de um login novo (ssh NOME@vps, depois sudo cais host confirm): e ele
que prova que a chave funciona. Da sessao do root, recusa.`},
	}},
	{title: "Setup da VPS (so configura; os servicos ligam com cais service):", group: "setup", cmds: []command{
		{name: "setup plan", usage: "setup", short: "mostra a config, o que falta e o que aplicar", long: `
Nao muda nada. Mostra a configuracao, o que falta informar e, dos servicos
ligados, quais tem config salva ainda nao aplicada (aplique com
"cais service <nome> on").`},
		{name: "setup config", usage: "setup config [opcoes]", short: "grava o /etc/cais/config.yaml (so o que for passado)\n    --email --tls letsencrypt|selfsigned --test-domain --http-port --https-port\n    --dns cloudflare|none --wildcards --behind-proxy --github-app-id --host-s3\n    --notify-from --notify-to", long: `
So o que for passado muda; o resto fica como esta no arquivo. Gravar nao
sobe nada: vale ao ligar um servico, ou ao aplicar num que ja esta ligado
("cais service proxy on" de novo). Nao ha dominio da VPS: cada app traz o
proprio dominio no compose, e o certificado do Let's Encrypt sai pra ele.

Flags:
  --email e-mail          avisos do Let's Encrypt
  --tls modo              letsencrypt (padrao) ou selfsigned (teste local)
  --test-domain dominio   so no modo de teste: os nomes que a CA local assina
                          (ex: trd.localhost; apps em <app>.trd.localhost)
  --dns cloudflare|none   como o Let's Encrypt confere o dominio: none (padrao)
                          acessa o site pela porta 80; cloudflare cria um
                          registro TXT com o token da API (cais setup secret
                          cloudflare_dns_token, permissao Zone > DNS > Edit).
                          Pelo DNS sai certificado ate pra dominio que a
                          internet nao alcanca (IP do Tailscale); todo
                          dominio dos apps precisa estar na Cloudflare
  --wildcards dominios    certificado curinga, separados por virgula:
                          dev.exemplo.com.br cobre *.dev.exemplo.com.br (os
                          apps debaixo dele ja nascem com HTTPS); com --dns
  --http-port porta       porta http do proxy no host (padrao 80)
  --https-port porta      porta https do proxy no host (padrao 443); fora de
                          80/443 o Let's Encrypt nao emite (a nao ser pelo
                          --dns): so pra teste
  --behind-proxy          migracao com outro proxy da VPS (nginx) nas 80/443:
                          as portas escutam so no 127.0.0.1 e o Traefik aceita
                          o X-Forwarded-For dele (=false desliga, pra voltar
                          pras 80/443). Ver docs/setup.md, "Migrar de um nginx"
  --github-app-id id      App ID do GitHub App do cais (baixa o codigo dos apps)
  --host-s3 host          endpoint S3 do Garage (obrigatorio com o Garage)
  --notify-from e-mail    remetente dos avisos (um remetente validado na Brevo)
  --notify-to e-mails     destinatarios dos avisos, separados por virgula;
                          vazio desliga. A chave fica no secret brevo_api_key

Os avisos sao e-mails pela Brevo quando um deploy automatico falha de vez
(sem nova tentativa marcada) ou quando o backup automatico nao da certo.

Exemplos:
  cais setup config --email voce@exemplo.com.br --github-app-id 123456
  cais setup config --tls selfsigned --test-domain trd.localhost   # teste no PC
  cais setup secret cloudflare_dns_token        # cola o token (sem eco)
  cais setup config --dns cloudflare --wildcards dev.exemplo.com.br && cais service proxy on
  cais setup config --http-port 8080 --https-port 8443 && cais service proxy on
  cais setup config --http-port 8080 --https-port 8443 --behind-proxy   # atras do nginx`},
		{name: "setup secret", usage: "setup secret <nome>", short: "grava um secret informado (le do stdin), ex: github_app_key", long: `
No terminal le sem eco; com stdin redirecionado, le o arquivo.

Exemplo:
  cais setup secret github_app_key < chave.pem
  cais setup secret brevo_api_key            # cola a chave xkeysib-...`},
		{name: "setup notify-test", usage: "setup notify-test", short: "manda um e-mail de teste pros destinatarios dos avisos", long: `
Usa o remetente e os destinatarios da config (cais setup config
--notify-from --notify-to) e a chave da Brevo (cais setup secret
brevo_api_key). Se a Brevo recusar, o motivo aparece aqui.`},
		{name: "setup github-connect", usage: "setup github-connect [-org ORG]", short: "cria o GitHub App pelo navegador e grava o App ID e a chave", long: `
Mostra um link: ele abre o GitHub com o app do cais ja preenchido (privado,
so le o codigo, sem webhook), na sua conta ou na organizacao do -org. Depois
do Create GitHub App, a pagina mostra um codigo: cole aqui. O cais troca o
codigo (vale 1 hora, uma vez) pelo App ID e a chave e grava os dois. Falta
instalar o app nos repositorios: o link sai no fim. O mesmo pela TUI:
Setup > Geral > Conectar ao GitHub.

Flags:
  -org ORG   organizacao dona do app (padrao: a sua conta)`},
		{name: "setup github-test", usage: "setup github-test", short: "confere o GitHub App salvo e diz onde ele esta instalado", long: `
Usa o App ID da config e a chave (cais setup secret github_app_key). Se o
GitHub recusar, o motivo aparece aqui; nao abre nenhum repositorio.`},
	}},
	{title: "Servicos (nada sobe sem ser ligado):", cmds: []command{
		{name: "service", usage: "service <nome> on|off|start|stop|restart", short: "liga/desliga, inicia/para ou reinicia um servico", long: `
Ligar gera os secrets que faltam, grava o compose e sobe o servico (e
mostra o progresso, tambem na TUI aberta). "on" num servico ja ligado
aplica nele a config salva no Setup. Desligar derruba os containers e
preserva os volumes (os dados); ligar de novo traz tudo de volta.

start, stop e restart so mexem nos containers de um servico ligado:
parado, ele continua ligado na config e volta no start (ou no proximo
"on"). Parar o postgres ou o mysql derruba os apps que usam o banco e faz
o backup falhar ate ele voltar.

O proxy (Traefik) publica os apps com HTTPS: sem ele, app com dominio nao
sobe. O Garage precisa do proxy ligado.

Exemplos:
  cais service proxy on
  cais service postgres on
  cais service postgres restart
  cais service garage off`},
	}},
	{title: "Bancos (no Postgres/MySQL da infra):", group: "db", cmds: []command{
		{name: "db create", usage: "db create postgres|mysql <nome> [-extensions postgis] [-import]", short: "database e usuario com senha nova (vazio ou importado)", long: `
Cria o database e um usuario de mesmo nome, que so acessa esse database. A
senha fica guardada no cais e chega no app pelas variaveis ao ligar o banco
(cais app db). Nome: minusculas, numeros e _, ate 32.

Com -import, o database novo ja nasce com o conteudo de um banco em outro
servidor: o cais pede a string de conexao (sem eco; ou le do stdin) e copia
tudo com pg_dump/mysqldump, de dentro do container do banco. No Postgres as
tabelas passam a ser do usuario do app; no MySQL o DEFINER das views e
triggers vira o do cais. Se a copia falhar, o database novo e apagado.

  postgres://usuario:senha@host:5432/banco?sslmode=require
  mysql://usuario:senha@host:3306/banco

O servidor de origem tem que aceitar conexao vinda do IP desta VPS. A
origem so e lida (nada muda nela). Banco no proprio servidor: use o IP dele
na rede do docker (ex: 172.17.0.1), nao localhost.

Flags:
  -extensions lista   extensoes do Postgres, separadas por virgula:
` + wrapList(databases.Extensions, 22, 78) + `
  -import             importa de outro servidor (pede a string de conexao)

Exemplos:
  cais db create postgres portal -extensions postgis
  cais db create mysql estoque -import
  cais app db portal postgres/portal      # liga ao app`},
		{name: "db list", usage: "db list", short: "databases criados pelo cais: variaveis, espaco e app"},
		{name: "db console", usage: "db console postgres|mysql [database]", short: "abre o psql ou o mysql, como administrador", long: `
Abre o cliente do banco neste terminal, dentro do container (psql como o
usuario postgres; mysql como root), no database pedido ou no padrao. Sai
com \q, exit ou ctrl+d. O mesmo na TUI: aba Infra, card do banco, botao
psql ou mysql.

Precisa de um terminal: pelo ssh, use -t.

Exemplos:
  cais db console postgres
  cais db console postgres loja
  ssh -t cais@vps db console mysql`},
		{name: "db delete", usage: "db delete postgres|mysql <nome> [-yes]", short: "apaga o database e o usuario (nao pode estar ligado a app)", long: `
Apaga todos os dados do database, o usuario e a senha guardada. Nao tem
volta. Recusa se algum app ainda usa o banco: desligue antes com
"cais app db <app> off postgres/<nome>". Pede o nome digitado pra confirmar.

Flags:
  -yes   nao pede confirmacao`},
	}},
	{title: "Buckets (no Garage da infra):", group: "bucket", cmds: []command{
		{name: "bucket create", usage: "bucket create <nome>", short: "bucket e uma chave so dele (guardada no cais)", long: `
Cria o bucket (privado) e uma chave com leitura, escrita e dono so nele. A
chave fica guardada no cais e chega no app pelas variaveis ao ligar o bucket
(cais app bucket). Nome: 3 a 63 letras minusculas, numeros e -.

Os arquivos dos buckets nao entram no backup do cais (so os databases e o
estado do cais entram).

Exemplos:
  cais bucket create portal-fotos
  cais app bucket portal portal-fotos     # liga ao app`},
		{name: "bucket list", usage: "bucket list", short: "buckets criados pelo cais: variaveis, espaco e app"},
		{name: "bucket delete", usage: "bucket delete <nome> [-yes]", short: "apaga o bucket (vazio) e a chave (nao pode estar ligado a app)", long: `
O Garage so apaga bucket vazio. Recusa se algum app ainda usa o bucket:
desligue antes com "cais app bucket <app> off <nome>". Pede o nome digitado
pra confirmar.

Flags:
  -yes   nao pede confirmacao`},
	}},
	{title: "Backup (bancos e estado do cais, criptografado, pro Google Drive ou OneDrive):", group: "backup", cmds: []command{
		{name: "backup status", usage: "backup status", short: "configuracao, proximo e ultimo backup", long: `
Mostra a configuracao, o proximo e o ultimo backup, e o que fica de fora:
os arquivos do Garage (buckets S3) e os volumes dos apps nao entram no
backup do cais.`},
		{name: "backup provider", usage: "backup provider google|onedrive", short: "escolhe o destino: Google Drive ou OneDrive pessoal", long: `
Client e login de cada destino ficam guardados: voltar pro outro nao pede
login de novo. Os backups ja feitos ficam onde estao; files, restore e a
retencao olham so o destino atual.`},
		{name: "backup client", usage: "backup client <client-id>", short: "grava o client do destino (Google Cloud ou Azure)", long: `
Google Drive: o client e do seu projeto no Google Cloud. Ative a Google
Drive API, crie em Credenciais -> ID do cliente OAuth do tipo "TVs e
dispositivos de entrada limitada" e publique a tela de consentimento (em
"Teste" o login expira em 7 dias). O escopo e drive.file: o cais so ve a
pasta que ele mesmo cria. O secret e lido sem eco, ou do stdin (cais backup
client <id> < arquivo).

OneDrive (conta pessoal): no portal do Azure, em Registros de aplicativo,
registre um app com "Somente contas pessoais da Microsoft" e, em
Autenticacao, ligue "Permitir fluxos de clientes publicos". O <client-id> e
o "ID do aplicativo (cliente)"; nao tem secret. O cais pede so a pasta do
app (Files.ReadWrite.AppFolder): fica em Aplicativos/<nome do app> e o resto
do OneDrive nao e visto.`},
		{name: "backup login", usage: "backup login", short: "conecta o destino (um codigo pra digitar em outro aparelho)"},
		{name: "backup logout", usage: "backup logout", short: "desconecta o destino (os backups ficam la)"},
		{name: "backup key", usage: "backup key [-yes]", short: "gera a chave e mostra a privada uma vez", long: `
A publica fica na config e criptografa os backups; a privada aparece so
agora e nao fica na VPS: guarde num gerenciador de senhas. Sem ela nenhum
backup abre. Trocar a chave nao reabre os backups antigos (eles continuam com
a privada anterior).`},
		{name: "backup schedule", usage: "backup schedule HH:MM|off [-days 30]", short: "horario diario (fuso da VPS) e retencao", long: `
O backup roda todo dia no horario; se a VPS estava desligada na hora, roda
assim que o daemon subir. Os backups mais velhos que -days dias saem do
destino (o mais novo fica sempre). "off" deixa so o backup manual.

Exemplo:
  cais backup schedule 03:00 -days 30`},
		{name: "backup now", usage: "backup now", short: "faz um backup agora"},
		{name: "backup files", usage: "backup files", short: "lista os backups no destino"},
		{name: "backup restore", usage: "backup restore <backup> -key <arquivo> [engine/database ...] [-yes]", short: "devolve databases ao estado do backup", long: `
Sem databases, so lista o que o backup tem. Com databases, apaga o
conteudo atual de cada um (as conexoes abertas caem), recria com o mesmo
usuario e senha e poe o dump do backup. O database tem que existir no cais.
A chave privada so passa pela memoria do daemon.

Exemplos:
  cais backup files
  cais backup restore cais-vps-2026-11-13_0300.tar.gz.age -key chave.txt
  cais backup restore cais-vps-2026-11-13_0300.tar.gz.age -key chave.txt postgres/loja`},
		{name: "backup open", usage: "backup open <arquivo> -key <arquivo> -to <pasta>", short: "extrai um backup baixado (sem o daemon, ate numa VPS nova)", long: `
Abre um backup baixado do Drive (ou OneDrive) numa pasta: os dumps (postgres/, mysql/) e
o estado do cais (state/etc = /etc/cais, state/data = /var/lib/cais, com os
secrets). Serve pra olhar um dump ou montar uma VPS nova.`},
	}},
	{title: "Apps (repositorio no GitHub com o compose de producao pronto):", group: "app", cmds: []command{
		{name: "apps", usage: "apps", short: "lista os apps e o estado dos containers"},
		{name: "app create", usage: "app create <nome> <dono/repo> [-branch b] [-compose arquivo] [-promote-to app] [-auto on]", short: "cadastra o app (nao faz deploy)", long: `
O repositorio pode vir como dono/repo ou link do GitHub. O codigo e baixado
pelo GitHub App do cais. Nome do app: minusculas, numeros e -.

Flags:
  -branch branch     branch do deploy (padrao: a principal do repositorio)
  -compose arquivo   compose de producao, a partir da raiz (padrao docker-compose.yml)
  -promote-to app    o app novo e a homologacao deste (mesmo repositorio): o
                     commit validado nele vai pra producao com cais app promote
  -auto on           deploy automatico a cada commit novo na branch

Exemplos:
  cais app create portal trdSystemDev/portal-web -branch main -compose docker-compose.cais.yml
  cais app create portal-homolog trdSystemDev/portal-web -branch homolog \
      -compose docker-compose.homolog.yaml -promote-to portal -auto on
  cais app guide portal                 # como escrever o compose`},
		{name: "app set", usage: "app set <nome> [-branch b] [-compose arquivo] [-promote-to <app>|off] [-auto on|off]", short: "troca branch, compose, producao (homolog)\ne deploy automatico", long: `
So muda o que vier nas flags; vale no proximo deploy.

Deploy automatico: o daemon consulta o GitHub a cada minuto (nenhuma porta
abre) e faz o deploy quando a branch ganha um commit novo. Um commit que
falhou nao e tentado de novo; o proximo commit sim.

Combinacoes comuns:
  so producao, manual             (padrao)
  so producao, automatica         cais app set portal -auto on
  homolog automatica, promocao manual
                                  cais app set portal-homolog -auto on
  homolog automatica e producao automatica no merge na main
                                  cais app set portal-homolog -auto on
                                  cais app set portal -auto on   (branch main)

Exemplos:
  cais app set portal-homolog -promote-to portal
  cais app set portal -auto off`},
		{name: "app promote", usage: "app promote <homologacao> [-yes]", short: "sobe na producao o commit validado na homologacao", long: `
Sobe na producao o commit que esta no ar (e de pe) na homologacao, pelo
deploy normal: build com a versao atual no ar e, se falhar, a anterior volta.

Vale pra homologacao na mesma branch da producao (um commit a frente) ou numa
branch propria: o commit validado tem que estar na branch da producao (faca o
merge antes; squash merge tambem vale, pelo conteudo). Assim um deploy futuro
da branch nao desfaz a promocao.

Flags:
  -yes   aceita voltar a producao pra um commit mais velho

Exemplos:
  cais app promote portal-homolog`},
		{name: "app deploy", usage: "app deploy <nome> [-ref <branch ou commit>]", short: "baixa o commit, build com a versao no ar, sobe;\nse o up falhar, a versao anterior volta", long: `
Recusa compose fora do formato do cais antes do build, dizendo o motivo.

Flags:
  -ref ref   branch ou commit (padrao: o mais novo da branch do app)

Exemplos:
  cais app deploy portal
  cais app deploy portal -ref 1910cca`},
		{name: "app history", usage: "app history <nome>", short: "os ultimos deploys: quando, commit, origem e resultado"},
		{name: "app rollback", usage: "app rollback <nome> [commit]", short: "volta pra um deploy anterior que deu certo (sem build)", long: `
Sem commit, volta pro deploy anterior ao que esta no ar. O commit tem que
estar no historico (cais app history) com deploy que deu certo; pra qualquer
outro, use cais app deploy -ref.

Os ultimos 5 deploys que deram certo guardam as imagens: a volta nao refaz o
build e passa pela troca sem queda. As variaveis e os secrets sao os de
agora. Com deploy automatico ligado, ele espera um commit novo na branch (o
que estava no ar antes do rollback nao sobe sozinho).

Exemplos:
  cais app rollback portal
  cais app rollback portal 1910cca`},
		{name: "app guide", usage: "app guide <nome>", short: "modelo de compose: labels do Traefik, redes, cais run"},
		{name: "app env", usage: "app env <nome> [VAR=valor... | VAR] [-unset VAR,...] [-import arquivo.env] [-show]", short: "mostra ou muda as variaveis (senhas inclusive);\nVAR sozinho le o valor sem eco", long: `
Sem mudancas, mostra os nomes (-show mostra os valores). Todas as variaveis,
senhas e chaves inclusive, chegam no processo do app pelo cais run (veja
cais app guide): nada vai pro docker inspect. Valem no proximo deploy.

VAR sozinho (sem =) pede o valor sem eco, ou le do stdin: senha fora do
historico do shell, ou um valor com varias linhas (chave PEM, JSON).

Flags:
  -unset VAR,...      remove as variaveis
  -import arquivo     junta um .env (- le do stdin); a que ja existe recebe o valor novo
  -show               mostra os valores

Exemplos:
  cais app env portal
  cais app env portal NODE_ENV=production DOMINIO=portal.exemplo.com
  cais app env portal JWT_SECRET                  # pede o valor sem eco
  cais app env portal FIREBASE_KEY < chave.json
  cais app env portal -unset TESTE
  cais app env portal -import .env.producao`},
		{name: "app db", usage: "app db <nome> [postgres/<db> | off postgres/<db>]", short: "bancos da infra do app: o deploy entrega as variaveis\nPOSTGRES_<DB>_USER, _PASS e _URL", long: `
Sem banco, mostra os ligados. O database tem o nome do usuario; a URL vem
pronta (postgres://...@postgres:5432/<db>, ou mysql:3306). Desligar nao apaga
o database.

Exemplos:
  cais app db portal postgres/portal
  cais app db portal off postgres/portal`},
		{name: "app bucket", usage: "app bucket <nome> [<bucket> | off <bucket>]", short: "buckets do app: o deploy entrega as variaveis\nS3_<BUCKET>_ACCESS_KEY, _SECRET_KEY e _ENDPOINT", long: `
Sem bucket, mostra os ligados. O - do nome do bucket vira _ nas variaveis.
Endpoint http://garage:3900, regiao garage, path-style, na rede
garage_network. Desligar nao apaga o bucket.

Exemplos:
  cais app bucket portal portal-fotos
  cais app bucket portal off portal-fotos`},
		{name: "app restart", usage: "app restart <nome> [service]", short: "reinicia o app (ou um service)"},
		{name: "app stop", usage: "app stop <nome>", short: "para o app sem apagar nada", long: `
Volta com: cais app restart <nome>`},
		{name: "app logs", usage: "app logs <nome> <container>", short: "ultimas linhas do log", long: `
O nome do container aparece em "cais apps" (ex: portal-web-1).`},
		{name: "app shell", usage: "app shell <nome> [service]", short: "shell interativo (bash ou sh) num container do app", long: `
Abre o bash do container (ou o sh, se nao tiver bash) neste terminal, como
um docker exec -it. Sai com exit ou ctrl+d. Sem o service, vale quando o
app tem um container so rodando; com varios, diga qual (o service do
compose, ex: web, ou o nome do container). Imagem sem shell (distroless,
scratch) nao abre. O mesmo na TUI: aba Apps, secao Logs, botao Shell.

Precisa de um terminal: pelo ssh, use -t.

Exemplos:
  cais app shell loja
  cais app shell loja worker
  ssh -t cais@vps app shell loja`},
		{name: "app delete", usage: "app delete <nome> [-volumes] [-yes]", short: "exclui o app (containers, imagens, codigo, variaveis,\nsecrets); -volumes apaga tambem os dados. O database\nda infra fica", long: `
Pede o nome digitado pra confirmar. Bancos e buckets da infra ficam.

Flags:
  -volumes   apaga tambem os volumes do compose (os dados)
  -yes       nao pede confirmacao`},
	}},
}

// wrapList escreve os itens separados por virgula, com recuo, em linhas de
// ate width colunas.
func wrapList(items []string, indent, width int) string {
	var lines []string
	line := strings.Repeat(" ", indent)
	for i, it := range items {
		if i < len(items)-1 {
			it += ","
		}
		if len(line)+len(it)+1 > width && strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimRight(line, " "))
			line = strings.Repeat(" ", indent)
		}
		line += it + " "
	}
	return strings.Join(append(lines, strings.TrimRight(line, " ")), "\n")
}

// overview e o cais help: todos os comandos com o resumo.
func overview() string {
	var b strings.Builder
	b.WriteString("cais -- painel da VPS\n")
	for _, s := range sections {
		writeSection(&b, s)
	}
	b.WriteString("\nDetalhes de um comando: cais <comando> -h (ex: cais app deploy -h).\n" + socketNote + "\n")
	return b.String()
}

func writeSection(b *strings.Builder, s section) {
	b.WriteString("\n" + s.title + "\n")
	for _, c := range s.cmds {
		usage := strings.TrimSpace("cais " + c.usage)
		lines := strings.Split(c.short, "\n")
		if len(usage) <= 33 {
			fmt.Fprintf(b, "  %-34s %s\n", usage, lines[0])
			lines = lines[1:]
		} else {
			fmt.Fprintf(b, "  %s\n", usage)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "    ") {
				fmt.Fprintf(b, "  %s\n", l) // flags listadas embaixo do uso
				continue
			}
			fmt.Fprintf(b, "%37s%s\n", "", l)
		}
	}
}

// helpFor e o -h de um comando ("app deploy"), de um grupo ("app") ou, se
// nao achar, o cais help inteiro.
func helpFor(name string) string {
	name = strings.TrimSpace(name)
	for _, s := range sections {
		if s.group != "" && s.group == name {
			var b strings.Builder
			writeSection(&b, s)
			fmt.Fprintf(&b, "\nDetalhes: cais %s <comando> -h.\n%s\n", s.group, socketNote)
			return strings.TrimPrefix(b.String(), "\n")
		}
		for _, c := range s.cmds {
			if c.name == name && c.name != "tui" {
				var b strings.Builder
				fmt.Fprintf(&b, "Uso: cais %s\n\n", c.usage)
				var parts []string
				for _, l := range strings.Split(c.short, "\n") {
					if !strings.HasPrefix(l, "    ") { // flags: ficam no long
						parts = append(parts, l)
					}
				}
				short := strings.Join(parts, "\n")
				b.WriteString(strings.ToUpper(short[:1]) + short[1:] + ".\n")
				if c.long != "" {
					b.WriteString(c.long + "\n")
				}
				b.WriteString("\n" + socketNote + "\n")
				return b.String()
			}
		}
	}
	return overview()
}

// usageOf e a linha de uso de um comando, pras mensagens de erro.
func usageOf(name string) string {
	for _, s := range sections {
		for _, c := range s.cmds {
			if c.name == name {
				return "cais " + c.usage
			}
		}
	}
	return "cais " + name
}

// groupError e o erro de subcomando faltando ou desconhecido, com os
// comandos do grupo.
func groupError(group, sub string) error {
	if sub == "" {
		return fmt.Errorf("falta o comando de %s\n\n%s", group, helpFor(group))
	}
	return fmt.Errorf("comando desconhecido: %s %s\n\n%s", group, sub, helpFor(group))
}
