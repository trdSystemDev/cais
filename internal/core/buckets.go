package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/trdSystemDev/cais/internal/apps"
	"github.com/trdSystemDev/cais/internal/buckets"
	"github.com/trdSystemDev/cais/internal/config"
	"github.com/trdSystemDev/cais/internal/secrets"
)

// BucketInfo e um bucket do Garage criado pelo cais. A chave fica guardada
// no cais e so chega no app, pelos secrets.
type BucketInfo struct {
	Name string `json:"name"`
	// Endpoint e Region sao como o app chega no S3, pela rede Network
	// (enderecamento path-style).
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Network  string `json:"network"`
	// Secrets sao os secrets que o app ligado recebe
	// (s3_<bucket>_access_key e s3_<bucket>_secret_key).
	Secrets []string `json:"secrets"`
	// Apps sao os apps ligados ao bucket.
	Apps []string `json:"apps,omitempty"`
	// Size e o espaco ocupado, como o Garage escreve; vazio se ele nao
	// respondeu.
	Size string `json:"size,omitempty"`
}

// a chave de cada bucket fica em dois secrets centrais:
// bucket_<nome>_key_id e bucket_<nome>_secret_key (- vira _)
func bucketSecrets(name string) (id, secret string) {
	n := "bucket_" + strings.ReplaceAll(name, "-", "_")
	return n + "_key_id", n + "_secret_key"
}

func bucketInfo(name string) BucketInfo {
	return BucketInfo{Name: name, Endpoint: buckets.Endpoint, Region: buckets.Region, Network: buckets.Network,
		Secrets: apps.BucketLink{Name: name}.Secrets()}
}

// bucketCredentials e a chave de um bucket criado pelo cais (o que o app
// recebe).
func (c *Core) bucketCredentials(name string) (string, string, error) {
	idName, secretName := bucketSecrets(name)
	id, err := c.secretStore().Get(idName)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", "", fmt.Errorf("o bucket %s nao existe no cais (crie na aba Infra -> Garage -> Novo bucket)", name)
	}
	if err != nil {
		return "", "", err
	}
	secret, err := c.secretStore().Get(secretName)
	if err != nil {
		return "", "", fmt.Errorf("chave do bucket %s: %w", name, err)
	}
	return string(id), string(secret), nil
}

// CreateBucket cria o bucket no Garage e uma chave so dele. Nome que ja
// existe e recusado.
func (c *Core) CreateBucket(ctx context.Context, name string) (BucketInfo, error) {
	name = strings.TrimSpace(name)
	if err := buckets.CheckName(name); err != nil {
		return BucketInfo{}, err
	}
	cfg, err := config.Load(c.deps.ConfigPath)
	if err != nil {
		return BucketInfo{}, err
	}
	if !cfg.HasStack("garage") {
		return BucketInfo{}, errors.New("a stack garage esta desligada: ligue na aba Infra")
	}
	if !c.infra.TryLock() {
		return BucketInfo{}, ErrBusy
	}
	defer c.infra.Unlock()

	store := c.secretStore()
	idName, secretName := bucketSecrets(name)
	if ok, err := secrets.Exists(store, idName); err != nil {
		return BucketInfo{}, err
	} else if ok {
		return BucketInfo{}, fmt.Errorf("o bucket %s ja existe (esta na lista)", name)
	}
	key, err := buckets.Create(ctx, c.deps.Exec, name)
	if err != nil {
		return BucketInfo{}, err
	}
	if err := store.Put(secretName, []byte(key.Secret)); err != nil {
		return BucketInfo{}, fmt.Errorf("bucket criado, mas a chave nao foi guardada: %w", err)
	}
	// o key_id por ultimo: e ele que marca o bucket como criado pelo cais
	if err := store.Put(idName, []byte(key.ID)); err != nil {
		return BucketInfo{}, fmt.Errorf("bucket criado, mas a chave nao foi guardada: %w", err)
	}
	return bucketInfo(name), nil
}

// DeleteBucket apaga o bucket, a chave dele e a credencial guardada. O
// Garage so apaga bucket vazio. Recusa se algum app ainda usa o bucket.
func (c *Core) DeleteBucket(ctx context.Context, name string) error {
	store := c.secretStore()
	idName, secretName := bucketSecrets(name)
	id, err := store.Get(idName)
	if errors.Is(err, secrets.ErrNotFound) {
		return fmt.Errorf("o bucket %s nao existe no cais", name)
	}
	if err != nil {
		return err
	}
	users, err := c.appsUsing(func(a apps.App) bool {
		return slices.ContainsFunc(a.Buckets, func(l apps.BucketLink) bool { return l.Name == name })
	})
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return fmt.Errorf("o bucket %s esta ligado a %s: desligue em Apps -> Secrets antes de excluir", name, strings.Join(users, ", "))
	}
	if !c.infra.TryLock() {
		return ErrBusy
	}
	defer c.infra.Unlock()
	if err := buckets.Delete(ctx, c.deps.Exec, name, string(id)); err != nil {
		return err
	}
	if err := store.Delete(secretName); err != nil {
		return err
	}
	return store.Delete(idName)
}

// Buckets lista os buckets criados pelo cais, com os apps que usam cada um
// e o espaco ocupado.
func (c *Core) Buckets(ctx context.Context) ([]BucketInfo, error) {
	names, err := c.secretStore().List()
	if err != nil {
		return nil, err
	}
	var out []BucketInfo
	for _, n := range names {
		if rest, ok := strings.CutPrefix(n, "bucket_"); ok {
			if name, ok := strings.CutSuffix(rest, "_key_id"); ok {
				out = append(out, bucketInfo(strings.ReplaceAll(name, "_", "-")))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	list, err := c.appManager().List()
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, a := range list {
			for _, l := range a.Buckets {
				if l.Name == out[i].Name {
					out[i].Apps = append(out[i].Apps, a.Name)
				}
			}
		}
		// Garage fora do ar: lista sem o tamanho
		out[i].Size, _ = buckets.Size(ctx, c.deps.Exec, out[i].Name)
	}
	return out, nil
}

// LinkAppBucket liga o app a um bucket do Garage: o deploy grava
// ${CAIS_SECRETS}/s3_<bucket>_access_key e s3_<bucket>_secret_key.
func (c *Core) LinkAppBucket(ctx context.Context, app, bucket string) error {
	unlock, err := c.lockApp(app)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().LinkBucket(app, apps.BucketLink{Name: bucket})
	c.CheckApps(ctx)
	return err
}

// UnlinkAppBucket desliga o app do bucket; o bucket e os arquivos ficam.
func (c *Core) UnlinkAppBucket(ctx context.Context, app, bucket string) error {
	unlock, err := c.lockApp(app)
	if err != nil {
		return err
	}
	defer unlock()
	err = c.appManager().UnlinkBucket(app, bucket)
	c.CheckApps(ctx)
	return err
}
