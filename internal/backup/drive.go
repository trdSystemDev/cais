package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// DriveScope so da acesso aos arquivos que o proprio cais criou.
const DriveScope = "https://www.googleapis.com/auth/drive.file"

// GoogleEndpoint e o OAuth do Google com o fluxo de dispositivo.
var GoogleEndpoint = oauth2.Endpoint{
	AuthURL:       "https://accounts.google.com/o/oauth2/auth",
	TokenURL:      "https://oauth2.googleapis.com/token",
	DeviceAuthURL: "https://oauth2.googleapis.com/device/code",
}

// Client e o OAuth client do login (tipo "TVs e dispositivos de entrada
// limitada" no Google; app publico, sem secret, na Microsoft).
type Client struct {
	ID     string `json:"client_id"`
	Secret string `json:"client_secret"`
}

// GoogleClientID e GoogleClientSecret sao o OAuth client "cais" da TRD
// System no Google Cloud (tipo "TVs e dispositivos de entrada limitada", so
// o escopo drive.file, em producao): com ele conectar o Google Drive e so o
// login. Vem no build do release (-ldflags -X, dos secrets
// CAIS_GOOGLE_CLIENT_ID e CAIS_GOOGLE_CLIENT_SECRET do GitHub Actions), fora
// do codigo: o secret de um client desse tipo nao abre nada sem o login da
// pessoa, mas no repositorio publico o Google o trataria como vazado.
var GoogleClientID, GoogleClientSecret string

// GoogleClient e o client do Google do binario; erro no build sem ele (dev).
func GoogleClient() (Client, error) {
	if GoogleClientID == "" || GoogleClientSecret == "" {
		return Client{}, errors.New("este binario do cais foi compilado sem o client do Google (CAIS_GOOGLE_CLIENT_ID e CAIS_GOOGLE_CLIENT_SECRET no make): use o do release")
	}
	return Client{ID: GoogleClientID, Secret: GoogleClientSecret}, nil
}

// OAuth e a config do oauth2 pro client.
func (c Client) OAuth(endpoint oauth2.Endpoint) *oauth2.Config {
	return &oauth2.Config{ClientID: c.ID, ClientSecret: c.Secret, Endpoint: endpoint, Scopes: []string{DriveScope}}
}

// Drive fala com a API v3 do Google Drive. So o necessario: pasta, envio,
// lista, download e exclusao.
type Drive struct {
	HTTP *http.Client // ja autenticado (oauth2)
	// API e Upload trocam os enderecos nos testes.
	API    string
	Upload string
}

// NewDrive usa o token salvo; o oauth2 renova o acesso sozinho.
func NewDrive(ctx context.Context, src oauth2.TokenSource) *Drive {
	return &Drive{HTTP: oauth2.NewClient(ctx, src)}
}

func (d *Drive) api() string {
	if d.API != "" {
		return d.API
	}
	return "https://www.googleapis.com/drive/v3"
}

func (d *Drive) upload() string {
	if d.Upload != "" {
		return d.Upload
	}
	return "https://www.googleapis.com/upload/drive/v3"
}

// File e um arquivo no Drive.
type File struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size,string"`
	Created time.Time `json:"createdTime"`
	Trashed bool      `json:"trashed"`
}

// ErrNotFound e o arquivo ou pasta que nao existe (ou que o cais nao ve),
// no Drive ou no OneDrive.
var ErrNotFound = errors.New("nao encontrado na nuvem")

func (d *Drive) do(req *http.Request, out any) error {
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Google Drive: %s: %s", resp.Status, driveError(body))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// driveError tira a mensagem do erro JSON do Google.
func driveError(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return strings.TrimSpace(string(body))
}

func (d *Drive) json(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return d.do(req, out)
}

// Email e a conta conectada.
func (d *Drive) Email(ctx context.Context) (string, error) {
	var about struct {
		User struct {
			Email string `json:"emailAddress"`
		} `json:"user"`
	}
	err := d.json(ctx, "GET", d.api()+"/about?fields=user(emailAddress)", nil, &about)
	return about.User.Email, err
}

// Folder devolve a pasta id se ela existe (e nao esta na lixeira); senao
// cria uma pasta nova com name. O cais so ve a pasta que ele criou.
func (d *Drive) Folder(ctx context.Context, id, name string) (string, error) {
	if id != "" {
		var f File
		err := d.json(ctx, "GET", d.api()+"/files/"+url.PathEscape(id)+"?fields=id,trashed", nil, &f)
		if err == nil && !f.Trashed {
			return f.ID, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
	}
	var f File
	err := d.json(ctx, "POST", d.api()+"/files?fields=id", map[string]any{
		"name": name, "mimeType": "application/vnd.google-apps.folder",
	}, &f)
	return f.ID, err
}

// Put envia o arquivo (size bytes de r) pra pasta, pelo envio resumable:
// uma sessao e depois o conteudo inteiro num PUT.
func (d *Drive) Put(ctx context.Context, folder, name string, r io.Reader, size int64) (File, error) {
	meta, _ := json.Marshal(map[string]any{"name": name, "parents": []string{folder}})
	req, err := http.NewRequestWithContext(ctx, "POST", d.upload()+"/files?uploadType=resumable&fields=id,name,size,createdTime", bytes.NewReader(meta))
	if err != nil {
		return File{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return File{}, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return File{}, fmt.Errorf("Google Drive: abrindo o envio: %s", resp.Status)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return File{}, errors.New("Google Drive: o envio nao devolveu o endereco da sessao")
	}
	put, err := http.NewRequestWithContext(ctx, "PUT", loc, r)
	if err != nil {
		return File{}, err
	}
	put.ContentLength = size
	put.Header.Set("Content-Type", "application/octet-stream")
	var f File
	err = d.do(put, &f)
	return f, err
}

// List devolve os arquivos da pasta (fora da lixeira), do mais velho pro
// mais novo.
func (d *Drive) List(ctx context.Context, folder string) ([]File, error) {
	var all []File
	page := ""
	for {
		q := url.Values{
			"q":        {"'" + strings.ReplaceAll(folder, "'", `\'`) + "' in parents and trashed = false"},
			"fields":   {"nextPageToken,files(id,name,size,createdTime)"},
			"orderBy":  {"createdTime"},
			"pageSize": {"1000"},
		}
		if page != "" {
			q.Set("pageToken", page)
		}
		var res struct {
			Next  string `json:"nextPageToken"`
			Files []File `json:"files"`
		}
		if err := d.json(ctx, "GET", d.api()+"/files?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Files...)
		if page = res.Next; page == "" {
			return all, nil
		}
	}
}

// Delete apaga o arquivo de vez (sem passar pela lixeira).
func (d *Drive) Delete(ctx context.Context, id string) error {
	return d.json(ctx, "DELETE", d.api()+"/files/"+url.PathEscape(id), nil, nil)
}

// Get baixa o conteudo do arquivo em w.
func (d *Drive) Get(ctx context.Context, id string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", d.api()+"/files/"+url.PathEscape(id)+"?alt=media", nil)
	if err != nil {
		return err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Google Drive: %s: %s", resp.Status, driveError(body))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
