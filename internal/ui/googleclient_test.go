package ui

import "github.com/trdSystemDev/cais/internal/backup"

// o client do Google vem no build do release; nos testes, um de mentira
// (o drivetest nao confere)
func init() {
	backup.GoogleClientID, backup.GoogleClientSecret = "teste.apps.googleusercontent.com", "segredo-de-teste"
}
