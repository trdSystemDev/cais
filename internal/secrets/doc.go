// Package secrets gerencia os secrets centrais do cais: as senhas das
// stacks (root dos bancos, RPC e admin do Garage), as credenciais dos
// databases e buckets criados pelo cais e as chaves informadas (GitHub App,
// Brevo): gerar, copiar, propagar e normalizar permissoes.
//
// Os apps nao tem secrets aqui: tudo o que um app recebe e variavel (as
// dele e as dos bancos e buckets ligados), guardada em
// /var/lib/cais/apps/<app>/env e entregue no processo pelo cais run (veja
// internal/apps). Decisoes:
//
//   - Texto puro, nao criptografado em repouso: numa VPS sem TPM a chave
//     ficaria no mesmo disco, nao protege contra root e arrisca perder
//     todos os secrets numa migracao. A protecao e o /var/lib/cais 0700
//     do root; pastas de secrets 0700, arquivos 0444 (as stacks montam o
//     arquivo e podem rodar sem ser root).
//   - O arquivo precisa continuar no disco: o bind mount e refeito a cada
//     start do container (restart policy, reboot), e senhas de banco tem
//     que ser as mesmas na proxima subida.
//   - Nunca valor na config de um container ("environment:" do compose):
//     vaza no docker inspect. As stacks leem /run/secrets/<nome> ou *_FILE;
//     os apps recebem no processo, pelo cais run.
//   - O valor nunca sai do daemon por estado, log ou MCP: so o nome.
//   - Backups saem criptografados com age para uma chave publica; a
//     privada fica fora da VPS.
//   - Trocar um secret exige recriar o container: o bind mount de arquivo
//     prende o inode, e a gravacao atomica (rename) cria outro.
//
// O acesso passa por uma interface Store, pra um backend criptografado
// poder entrar depois sem mudar quem usa.
package secrets
