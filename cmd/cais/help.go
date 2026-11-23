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
                           negativo nao limita)`},
		{name: "state", usage: "state [--socket]", short: "imprime o estado atual em JSON"},
		{name: "check", usage: "check [--socket]", short: "checa o ambiente e imprime o resultado em JSON", long: `
Confere Docker e Docker Compose (instalados, versao, daemon no ar).`},
		{name: "mcp", usage: "mcp [--socket]", short: "servidor MCP pelo stdin/stdout (pra IA, via ssh)", long: `
Expoe o cais como ferramentas MCP: ler o estado, logs e a ajuda do compose;
criar e atualizar app, deploy, restart, stop, variaveis, secrets, criar e
ligar banco e bucket. Nada que apaga dados nem o Setup: excluir app, banco,
bucket ou secret, ligar ou desligar servico e a config da VPS ficam so na
TUI e no CLI.

Roda na VPS e conversa pelo stdin/stdout; o cliente MCP no seu computador
chama pelo ssh (a autenticacao e a sua chave SSH, sem porta nova).

Exemplo (Claude Code, no seu computador):
  claude mcp add cais -- ssh cais@vps cais mcp   # instalado com --ssh
  claude mcp add cais -- ssh root@vps cais mcp`},
		{name: "version", usage: "version", short: "mostra a versao"},
		{name: "help", usage: "help [comando]", short: "esta ajuda; com o comando, os detalhes dele", long: `
Exemplos:
  cais help app            comandos de app
  cais help app deploy     o mesmo que: cais app deploy -h`},
	}},
	{title: "Setup da VPS (so configura; os servicos ligam com cais service):", group: "setup", cmds: []command{
		{name: "setup plan", usage: "setup", short: "mostra a config, o que falta e o que aplicar", long: `
Nao muda nada. Mostra a configuracao, o que falta informar e, dos servicos
ligados, quais tem config salva ainda nao aplicada (aplique com
"cais service <nome> on").`},
		{name: "setup config", usage: "setup config [opcoes]", short: "grava o /etc/cais/config.yaml (so o que for passado)\n    --email --tls letsencrypt|selfsigned --test-domain --http-port --https-port\n    --github-app-id --host-s3 --notify-from --notify-to", long: `
So o que for passado muda; o resto fica como esta no arquivo. Gravar nao
sobe nada: vale ao ligar um servico, ou ao aplicar num que ja esta ligado
("cais service proxy on" de novo). Nao ha dominio da VPS: cada app traz o
proprio dominio no compose, e o certificado do Let's Encrypt sai pra ele.

Flags:
  --email e-mail          avisos do Let's Encrypt
  --tls modo              letsencrypt (padrao) ou selfsigned (teste local)
  --test-domain dominio   so no modo de teste: os nomes que a CA local assina
                          (ex: trd.localhost; apps em <app>.trd.localhost)
  --http-port porta       porta http do proxy no host (padrao 80)
  --https-port porta      porta https do proxy no host (padrao 443); fora de
                          80/443 o Let's Encrypt nao emite: so pra teste
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
  cais setup config --http-port 8080 --https-port 8443 && cais service proxy on`},
		{name: "setup secret", usage: "setup secret <nome>", short: "grava um secret informado (le do stdin), ex: github_app_key", long: `
No terminal le sem eco; com stdin redirecionado, le o arquivo.

Exemplo:
  cais setup secret github_app_key < chave.pem
  cais setup secret brevo_api_key            # cola a chave xkeysib-...`},
		{name: "setup notify-test", usage: "setup notify-test", short: "manda um e-mail de teste pros destinatarios dos avisos", long: `
Usa o remetente e os destinatarios da config (cais setup config
--notify-from --notify-to) e a chave da Brevo (cais setup secret
brevo_api_key). Se a Brevo recusar, o motivo aparece aqui.`},
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
senha fica guardada no cais e chega no app pelos secrets ao ligar o banco
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
		{name: "db list", usage: "db list", short: "databases criados pelo cais: secrets, espaco e app"},
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
chave fica guardada no cais e chega no app pelos secrets ao ligar o bucket
(cais app bucket). Nome: 3 a 63 letras minusculas, numeros e -.

Os arquivos dos buckets nao entram no backup do cais (so os databases e o
estado do cais entram).

Exemplos:
  cais bucket create portal-fotos
  cais app bucket portal portal-fotos     # liga ao app`},
		{name: "bucket list", usage: "bucket list", short: "buckets criados pelo cais: secrets, espaco e app"},
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
		{name: "app guide", usage: "app guide <nome>", short: "modelo de compose: labels do Traefik, redes, secrets"},
		{name: "app env", usage: "app env <nome> [VAR=valor...] [-unset VAR,...] [-import arquivo.env]", short: "mostra ou muda as variaveis (.env do projeto);\n-import junta um .env (- le do stdin)", long: `
Sem mudancas, so mostra. As variaveis viram o .env do projeto no deploy
(valem no proximo). Senhas e chaves vao em secrets, nao aqui.

Flags:
  -unset VAR,...      remove as variaveis
  -import arquivo     junta um .env (- le do stdin); a que ja existe recebe o valor novo

Exemplos:
  cais app env portal
  cais app env portal NODE_ENV=production DOMINIO=portal.exemplo.com
  cais app env portal -unset TESTE
  cais app env portal -import .env.producao`},
		{name: "app db", usage: "app db <nome> [postgres/<db> | off postgres/<db>]", short: "bancos da infra do app: o deploy grava os secrets\npostgres_<db>_user e postgres_<db>_pass em ${CAIS_SECRETS}", long: `
Sem banco, mostra os ligados. O database tem o nome do usuario; o app chega
por postgres:5432 (ou mysql:3306) e monta a URL. Desligar nao apaga o database.

Exemplos:
  cais app db portal postgres/portal
  cais app db portal off postgres/portal`},
		{name: "app bucket", usage: "app bucket <nome> [<bucket> | off <bucket>]", short: "buckets do app: o deploy grava os secrets\ns3_<bucket>_access_key e s3_<bucket>_secret_key", long: `
Sem bucket, mostra os ligados. O - do nome do bucket vira _ nos secrets. O
app usa o endpoint http://garage:3900, regiao garage, path-style, na rede
garage_network. Desligar nao apaga o bucket.

Exemplos:
  cais app bucket portal portal-fotos
  cais app bucket portal off portal-fotos`},
		{name: "app secrets", usage: "app secrets <nome>", short: "lista os secrets do app (so nomes)"},
		{name: "app secret", usage: "app secret <nome> <secret>", short: "grava um secret (le sem eco, ou do stdin)", long: `
Cada secret e um arquivo em ${CAIS_SECRETS}/<secret>; no container, em
/run/secrets/<secret>. Vale no proximo deploy. Nome: minusculas, numeros e _.

Exemplos:
  cais app secret portal jwt_secret            # pede o valor sem eco
  cais app secret portal firebase_key < chave.json`},
		{name: "app restart", usage: "app restart <nome> [service]", short: "reinicia o app (ou um service)"},
		{name: "app stop", usage: "app stop <nome>", short: "para o app sem apagar nada", long: `
Volta com: cais app restart <nome>`},
		{name: "app logs", usage: "app logs <nome> <container>", short: "ultimas linhas do log", long: `
O nome do container aparece em "cais apps" (ex: portal-web-1).`},
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
