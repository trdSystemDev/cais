// Package core e o nucleo do vpsctl: dono unico do estado e das acoes.
//
// Interfaces (TUI, MCP) nunca alteram estado nem falam com docker/disco
// direto: chamam metodos do Core e recebem fotos imutaveis do estado via
// Subscribe/Snapshot. Este pacote nao pode importar nada de interface.
package core
