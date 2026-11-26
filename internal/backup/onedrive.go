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
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// MicrosoftEndpoint e o login da Microsoft so pra contas pessoais
// (consumers: outlook.com, hotmail.com), com o fluxo de dispositivo. O app
// e publico: o registro no Azure nao tem secret.
var MicrosoftEndpoint = oauth2.Endpoint{
	AuthURL:       "https://login.microsoftonline.com/consumers/oauth2/v2.0/authorize",
	TokenURL:      "https://login.microsoftonline.com/consumers/oauth2/v2.0/token",
	DeviceAuthURL: "https://login.microsoftonline.com/consumers/oauth2/v2.0/devicecode",
	AuthStyle:     oauth2.AuthStyleInParams,
}

// OneDriveScopes: a pasta do app (Apps/<nome do app> no OneDrive), como o
// drive.file do Google: o cais nao ve o resto do OneDrive. offline_access
// da o refresh token; User.Read, o e-mail da conta.
var OneDriveScopes = []string{"Files.ReadWrite.AppFolder", "offline_access", "User.Read"}

// DefaultOneDriveClient e o app "cais" da TRD System no Azure (contas
// pessoais, cliente publico, fluxo de dispositivo): com ele conectar o
// OneDrive e so o login, sem registrar app. O ID nao e segredo (cliente
// publico); cada login so da acesso a pasta do app na conta de quem entrou.
const DefaultOneDriveClient = "71a420c4-b2f9-412b-978a-f21bfce04471"

// OneDriveOAuth e a config do oauth2 pro app do Azure (cliente publico:
// so o ID).
func OneDriveOAuth(clientID string, endpoint oauth2.Endpoint) *oauth2.Config {
	endpoint.AuthStyle = oauth2.AuthStyleInParams // cliente publico: sem Basic
	return &oauth2.Config{ClientID: clientID, Endpoint: endpoint, Scopes: OneDriveScopes}
}

// oneDriveChunk e o tamanho de cada pedaco do envio: multiplo de 320 KiB,
// como o Graph exige, e bem abaixo do maximo (60 MiB).
const oneDriveChunk = 32 * 320 * 1024 // 10 MiB

// OneDrive fala com o OneDrive pelo Microsoft Graph. So o necessario: pasta,
// envio, lista, download e exclusao, tudo dentro da pasta do app.
type OneDrive struct {
	HTTP *http.Client // ja autenticado (oauth2)
	// Plain manda os pedacos do envio e baixa o conteudo: as URLs ja vem
	// autorizadas e o Graph recusa o token nelas.
	Plain *http.Client
	// API troca o endereco do Graph nos testes.
	API string
	// Chunk troca o tamanho dos pedacos nos testes.
	Chunk int64
}

// NewOneDrive usa o token salvo; o oauth2 renova o acesso sozinho.
func NewOneDrive(ctx context.Context, src oauth2.TokenSource) *OneDrive {
	return &OneDrive{HTTP: oauth2.NewClient(ctx, src)}
}

func (o *OneDrive) api() string {
	if o.API != "" {
		return o.API
	}
	return "https://graph.microsoft.com/v1.0"
}

func (o *OneDrive) plain() *http.Client {
	if o.Plain != nil {
		return o.Plain
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (o *OneDrive) chunk() int64 {
	if o.Chunk > 0 {
		return o.Chunk
	}
	return oneDriveChunk
}

// item e um arquivo ou pasta no Graph (driveItem).
type item struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Created time.Time `json:"createdDateTime"`
	Folder  *struct{} `json:"folder,omitempty"`
}

func (it item) file() File { return File{ID: it.ID, Name: it.Name, Size: it.Size, Created: it.Created} }

func (o *OneDrive) check(resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		// o erro do Graph tem o mesmo formato do Google: {"error":{"message"}}
		return fmt.Errorf("OneDrive: %s: %s", resp.Status, driveError(body))
	}
	return nil
}

func (o *OneDrive) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	u := path
	if !strings.HasPrefix(u, "http") {
		u = o.api() + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := o.check(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Email e a conta conectada.
func (o *OneDrive) Email(ctx context.Context) (string, error) {
	var me struct {
		Mail string `json:"mail"`
		UPN  string `json:"userPrincipalName"`
	}
	if err := o.json(ctx, "GET", "/me?$select=mail,userPrincipalName", nil, &me); err != nil {
		return "", err
	}
	if me.Mail != "" {
		return me.Mail, nil
	}
	return me.UPN, nil
}

// Folder devolve a pasta id se ela ainda existe; senao a pasta name dentro
// da pasta do app, criada se preciso.
func (o *OneDrive) Folder(ctx context.Context, id, name string) (string, error) {
	if id != "" {
		var it item
		err := o.json(ctx, "GET", "/me/drive/items/"+url.PathEscape(id)+"?$select=id,folder", nil, &it)
		if err == nil && it.Folder != nil {
			return it.ID, nil
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return "", err
		}
	}
	var it item
	err := o.json(ctx, "GET", "/me/drive/special/approot:/"+url.PathEscape(name)+"?$select=id,folder", nil, &it)
	if err == nil && it.Folder != nil {
		return it.ID, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	err = o.json(ctx, "POST", "/me/drive/special/approot/children", map[string]any{
		"name": name, "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "rename",
	}, &it)
	return it.ID, err
}

// Put envia o arquivo pra pasta por uma sessao de envio, em pedacos (o
// Graph nao aceita mais que 60 MiB por requisicao).
func (o *OneDrive) Put(ctx context.Context, folder, name string, r io.Reader, size int64) (File, error) {
	var session struct {
		URL string `json:"uploadUrl"`
	}
	path := "/me/drive/items/" + url.PathEscape(folder) + ":/" + url.PathEscape(name) + ":/createUploadSession"
	if err := o.json(ctx, "POST", path, map[string]any{
		"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"},
	}, &session); err != nil {
		return File{}, fmt.Errorf("abrindo o envio: %w", err)
	}
	if session.URL == "" {
		return File{}, errors.New("OneDrive: o envio nao devolveu o endereco da sessao")
	}
	it, err := o.send(ctx, session.URL, r, size)
	if err != nil {
		// a sessao fica pendurada no OneDrive ate expirar: cancela
		if req, rerr := http.NewRequestWithContext(context.WithoutCancel(ctx), "DELETE", session.URL, nil); rerr == nil {
			if resp, derr := o.plain().Do(req); derr == nil {
				resp.Body.Close()
			}
		}
		return File{}, err
	}
	return it.file(), nil
}

// send manda os pedacos pra sessao; a resposta do ultimo e o arquivo.
func (o *OneDrive) send(ctx context.Context, session string, r io.Reader, size int64) (item, error) {
	buf := make([]byte, min(o.chunk(), max(size, 1)))
	for start := int64(0); start < size; {
		n, err := io.ReadFull(r, buf[:min(int64(len(buf)), size-start)])
		if err != nil {
			return item{}, err
		}
		end := start + int64(n) - 1
		req, err := http.NewRequestWithContext(ctx, "PUT", session, bytes.NewReader(buf[:n]))
		if err != nil {
			return item{}, err
		}
		req.ContentLength = int64(n)
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		resp, err := o.plain().Do(req)
		if err != nil {
			return item{}, err
		}
		if err := o.check(resp); err != nil {
			resp.Body.Close()
			return item{}, err
		}
		start = end + 1
		if start < size {
			resp.Body.Close() // 202: o proximo pedaco
			continue
		}
		var it item
		err = json.NewDecoder(resp.Body).Decode(&it)
		resp.Body.Close()
		return it, err
	}
	return item{}, errors.New("OneDrive: arquivo vazio")
}

// List devolve os arquivos da pasta (sem subpastas), do mais velho pro
// mais novo.
func (o *OneDrive) List(ctx context.Context, folder string) ([]File, error) {
	var all []File
	next := "/me/drive/items/" + url.PathEscape(folder) + "/children?$select=id,name,size,createdDateTime,folder&$top=200"
	for next != "" {
		var res struct {
			Value []item `json:"value"`
			Next  string `json:"@odata.nextLink"`
		}
		if err := o.json(ctx, "GET", next, nil, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Value {
			if it.Folder == nil {
				all = append(all, it.file())
			}
		}
		next = res.Next
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Created.Before(all[j].Created) })
	return all, nil
}

// Delete apaga o arquivo. No OneDrive pessoal ele vai pra lixeira, que se
// esvazia sozinha (e conta no espaco ate la).
func (o *OneDrive) Delete(ctx context.Context, id string) error {
	return o.json(ctx, "DELETE", "/me/drive/items/"+url.PathEscape(id), nil, nil)
}

// Get baixa o conteudo do arquivo em w. O Graph responde com um
// redirecionamento pra uma URL ja autorizada, que e baixada sem o token.
func (o *OneDrive) Get(ctx context.Context, id string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", o.api()+"/me/drive/items/"+url.PathEscape(id)+"/content", nil)
	if err != nil {
		return err
	}
	noFollow := *o.HTTP
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return err
	}
	if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 && loc != "" {
		resp.Body.Close()
		if req, err = http.NewRequestWithContext(ctx, "GET", loc, nil); err != nil {
			return err
		}
		if resp, err = o.plain().Do(req); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if err := o.check(resp); err != nil {
		return err
	}
	_, err = io.Copy(w, resp.Body)
	return err
}
