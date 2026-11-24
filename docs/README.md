# Documentacao do cais

- [Instalacao](instalacao.md): opcoes do instalador, atualizar, voltar versao, desinstalar
- [Seguranca da VPS](seguranca.md): preparar uma VPS nova (admin, SSH so com chave, firewall, fail2ban) e a checagem
- [Setup e configuracao](setup.md): proxy com HTTPS, servicos opcionais, `config.yaml`, avisos por e-mail
- [Apps](apps.md): deploy sem queda, formato do compose, historico e rollback, homologacao, deploy automatico
- [Bancos e storage](bancos-e-storage.md): databases (vazios ou importados de outro servidor) e buckets S3
- [Backups e disco](backups.md): backup criptografado pro Google Drive, restore, limpeza de disco
- [MCP](mcp.md): deploy e status pelo Claude Code
- [Desenvolvimento](desenvolvimento.md): arquitetura, testar no PC, build e releases

Ajuda de qualquer comando no proprio cais: `cais help`, `cais <grupo> -h`,
`cais <grupo> <comando> -h`.
