package backup

import (
	"context"
	"io"
	"log"
	"sync"

	"golang.org/x/oauth2"
)

// Destinos dos backups (config backup.provider).
const (
	ProviderGoogle   = "google"
	ProviderOneDrive = "onedrive"
)

// Providers sao os destinos aceitos, na ordem da tela.
var Providers = []string{ProviderGoogle, ProviderOneDrive}

// ProviderName e o nome do destino pra mostrar (vazio e o Google Drive, o
// padrao de antes de existir a escolha).
func ProviderName(p string) string {
	if p == ProviderOneDrive {
		return "OneDrive"
	}
	return "Google Drive"
}

// Store e onde os backups ficam: uma pasta do cais numa nuvem da pessoa
// (Google Drive ou OneDrive), em que ele so ve o que criou.
type Store interface {
	// Email e a conta conectada.
	Email(ctx context.Context) (string, error)
	// Folder devolve a pasta id se ela ainda existe; senao acha ou cria a
	// pasta name.
	Folder(ctx context.Context, id, name string) (string, error)
	// Put envia size bytes de r como name na pasta.
	Put(ctx context.Context, folder, name string, r io.Reader, size int64) (File, error)
	// List devolve os arquivos da pasta, do mais velho pro mais novo.
	List(ctx context.Context, folder string) ([]File, error)
	// Delete apaga o arquivo.
	Delete(ctx context.Context, id string) error
	// Get baixa o conteudo do arquivo em w.
	Get(ctx context.Context, id string, w io.Writer) error
}

var (
	_ Store = (*Drive)(nil)
	_ Store = (*OneDrive)(nil)
)

// SavingSource entrega os tokens de src e chama save quando o refresh
// token muda. A Microsoft troca o refresh token a cada renovacao e o antigo
// expira (90 dias): sem gravar o novo, o login cairia sozinho.
func SavingSource(src oauth2.TokenSource, refresh string, save func(*oauth2.Token) error) oauth2.TokenSource {
	return &savingSource{src: src, refresh: refresh, save: save}
}

type savingSource struct {
	mu      sync.Mutex
	src     oauth2.TokenSource
	refresh string
	save    func(*oauth2.Token) error
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if tok.RefreshToken != "" && tok.RefreshToken != s.refresh {
		if err := s.save(tok); err != nil {
			log.Printf("backup: gravando o login renovado: %v", err)
		} else {
			s.refresh = tok.RefreshToken
		}
	}
	return tok, nil
}
