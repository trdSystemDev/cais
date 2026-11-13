package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/trdSystemDev/cais/internal/backup"
	"github.com/trdSystemDev/cais/internal/core"
	"github.com/trdSystemDev/cais/internal/databases"
)

// runBackup configura e roda o backup (bancos e estado do cais, pro Google
// Drive) e devolve databases de um backup. "open" nao usa o daemon: abre um
// backup baixado, ate numa VPS nova.
func runBackup(args []string) error {
	sub, args := subcommand(args)
	fs, socket := newFlags("backup " + sub)
	keyFile := fs.String("key", "", "restore/open: arquivo com a chave privada (AGE-SECRET-KEY-...)")
	to := fs.String("to", "", "open: pasta onde extrair")
	days := fs.Int("days", 0, "schedule: retencao em dias (padrao 30)")
	yes := fs.Bool("yes", false, "restore/key: nao pede confirmacao")
	pos := parseInterspersed(fs, args)
	switch sub {
	case "status", "now", "files", "restore", "client", "login", "logout", "key", "schedule":
	case "open":
		return backupOpen(pos, *keyFile, *to)
	default:
		return groupError("backup", sub)
	}
	c, err := connect(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx := context.Background()

	switch sub {
	case "status":
		st, err := c.RefreshBackup(ctx)
		if err != nil {
			return err
		}
		printBackupStatus(st)
	case "client":
		if len(pos) != 1 {
			return errors.New("uso: cais backup client <client-id> (o secret e pedido sem eco ou lido do stdin)")
		}
		secret, err := readSecret("Client secret: ")
		if err != nil {
			return err
		}
		if err := c.SetBackupClient(ctx, pos[0], secret); err != nil {
			return err
		}
		fmt.Println("OAuth client gravado. Agora: cais backup login")
	case "login":
		return backupLogin(ctx, c)
	case "logout":
		if err := c.DisconnectDrive(ctx); err != nil {
			return err
		}
		fmt.Println("Drive desconectado (os backups que ja estao la ficam).")
	case "key":
		st, err := c.RefreshBackup(ctx)
		if err != nil {
			return err
		}
		if st.Recipient != "" && !*yes {
			if err := confirmName(false, "trocar", "Ja existe uma chave. Os backups antigos continuam abrindo so com a chave privada antiga."); err != nil {
				return err
			}
		}
		priv, err := c.NewBackupKey(ctx)
		if err != nil {
			return err
		}
		fmt.Println("Chave privada do backup (guarde num gerenciador de senhas; ela NAO fica na VPS e sem ela nenhum backup abre):")
		fmt.Println()
		fmt.Println(priv)
		fmt.Println()
	case "schedule":
		if len(pos) != 1 {
			return errors.New("uso: cais backup schedule HH:MM|off [-days 30]")
		}
		at := pos[0]
		if at == "off" {
			at = ""
		}
		if err := c.SetBackupSchedule(ctx, at, *days); err != nil {
			return err
		}
		st, _ := c.RefreshBackup(ctx)
		printBackupStatus(st)
	case "now":
		return withProgress(c, func(ctx context.Context) error {
			run, err := c.RunBackup(ctx)
			if run.File != "" && err == nil {
				fmt.Printf("Backup %s no Drive (%s, %d databases, %d arquivos)", run.File, databases.HumanSize(run.Size), run.Databases, run.Files)
				if run.Removed > 0 {
					fmt.Printf("; %d backups velhos apagados", run.Removed)
				}
				fmt.Println(".")
			}
			return err
		})
	case "files":
		files, err := c.BackupFiles(ctx)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			fmt.Println("Nenhum backup no Drive ainda. Faca um com: cais backup now")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "BACKUP\tTAMANHO\tCRIADO")
		for _, f := range files {
			fmt.Fprintf(w, "%s\t%s\t%s\n", f.Name, databases.HumanSize(f.Size), f.Created.Local().Format("02/01/2006 15:04"))
		}
		w.Flush()
	case "restore":
		if len(pos) < 1 {
			return errors.New("uso: cais backup restore <backup> -key <arquivo> [engine/database ...]")
		}
		identity, err := readKey(*keyFile)
		if err != nil {
			return err
		}
		req := core.RestoreRequest{File: pos[0], Identity: identity, Databases: pos[1:]}
		if len(req.Databases) > 0 && !*yes {
			if err := confirmName(false, "restaurar", "Restaurar apaga o conteudo atual de "+strings.Join(req.Databases, ", ")+
				" (as conexoes abertas caem) e poe o do backup no lugar."); err != nil {
				return err
			}
		}
		res, err := c.RestoreBackup(ctx, req)
		if err != nil {
			return err
		}
		if len(req.Databases) == 0 {
			fmt.Println("O backup tem: " + strings.Join(res.Available, ", "))
			fmt.Println("Restaure com: cais backup restore " + req.File + " -key <arquivo> engine/database ...")
			return nil
		}
		fmt.Println("Restaurado: " + strings.Join(res.Restored, ", "))
	}
	return nil
}

func printBackupStatus(st core.BackupStatus) {
	yes := func(ok bool, s string) string {
		if ok {
			return s
		}
		return "falta"
	}
	fmt.Printf("OAuth client:  %s\n", yes(st.ClientID != "", st.ClientID))
	fmt.Printf("Drive:         %s\n", yes(st.Email != "", st.Email))
	fmt.Printf("Chave:         %s\n", yes(st.Recipient != "", st.Recipient))
	when := "desligado (so manual)"
	if st.Time != "" {
		when = "todo dia as " + st.Time
	}
	fmt.Printf("Horario:       %s, retencao de %d dias\n", when, st.RetentionDays)
	if !st.Next.IsZero() {
		fmt.Printf("Proximo:       %s\n", st.Next.Local().Format("02/01/2006 15:04"))
	}
	if l := st.Last; l != nil {
		state := "ok"
		if !l.OK() {
			state = "com erro: " + l.Error
			if l.Error == "" {
				state = "sem " + strings.Join(l.Errors, "; ")
			}
		}
		fmt.Printf("Ultimo:        %s, %s (%s) %s\n", l.At.Local().Format("02/01/2006 15:04"), l.File, databases.HumanSize(l.Size), state)
	}
}

// backupLogin mostra o codigo e espera a pessoa liberar no navegador.
func backupLogin(ctx context.Context, c interface {
	StartDriveLogin(context.Context) (core.DriveLogin, error)
	CancelDriveLogin(context.Context) error
	Subscribe(context.Context) (<-chan core.State, func(), error)
}) error {
	states, stop, err := c.Subscribe(ctx)
	if err != nil {
		return err
	}
	defer stop()
	login, err := c.StartDriveLogin(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Abra %s em qualquer aparelho e digite o codigo:\n\n    %s\n\nAguardando (expira as %s)...\n", login.URL, login.Code, login.Expires.Local().Format("15:04"))
	seen := false
	for st := range states {
		b := st.Backup
		if b.Login != nil && b.Login.Code == login.Code {
			seen = true
			continue
		}
		if !seen {
			continue
		}
		if b.LoginError != "" {
			return errors.New(b.LoginError)
		}
		if b.Email != "" {
			fmt.Printf("Drive conectado: %s\n", b.Email)
			return nil
		}
	}
	return errors.New("conexao com o daemon caiu antes do login terminar")
}

// readKey le a chave privada do arquivo, ou do terminal sem eco.
func readKey(file string) (string, error) {
	if file == "" {
		return readSecret("Chave privada do backup (AGE-SECRET-KEY-...): ")
	}
	data, err := os.ReadFile(file)
	return strings.TrimSpace(string(data)), err
}

// backupOpen extrai um backup baixado numa pasta, sem o daemon: serve pra
// olhar um dump ou recuperar o estado do cais numa VPS nova.
func backupOpen(pos []string, keyFile, to string) error {
	if len(pos) != 1 || to == "" {
		return errors.New("uso: cais backup open <arquivo .tar.gz.age> -key <arquivo> -to <pasta>")
	}
	identity, err := readKey(keyFile)
	if err != nil {
		return err
	}
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := backup.NewReader(f, identity)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(to) // nada do arquivo escreve fora da pasta
	if err != nil {
		return err
	}
	defer root.Close()
	n := 0
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(strings.TrimSuffix(h.Name, "/"))
		mode := os.FileMode(h.Mode) & 0o777
		switch h.Typeflag {
		case '5': // pasta
			if err := root.MkdirAll(name, mode|0o700); err != nil {
				return err
			}
		case '0':
			if dir := filepath.Dir(name); dir != "." {
				if err := root.MkdirAll(dir, 0o700); err != nil {
					return err
				}
			}
			out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o200)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, r)
			out.Close()
			if err != nil {
				return err
			}
			root.Chmod(name, mode)
			n++
		}
	}
	fmt.Printf("%d arquivos extraidos em %s (em %s).\n", n, to, time.Now().Format("15:04:05"))
	fmt.Println("Dumps: postgres/<db>.sql e mysql/<db>.sql. Estado do cais: state/etc (config) e state/data (/var/lib/cais).")
	return nil
}
