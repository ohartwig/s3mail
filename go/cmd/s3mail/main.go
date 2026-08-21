// s3mail - Mail-Client fuer E-Mails, die Amazon SES als Rohdaten in einen
// S3-Bucket schreibt.
//
// Startet einen lokalen Webserver und oeffnet das Postfach im Browser. Ordner
// sind echte S3-Prefixe, der Zustand liegt geteilt im Bucket, Antworten laufen
// ueber SES.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"s3mail/assistent"
	"s3mail/awsx"
	"s3mail/konfig"
	"s3mail/store"
	"s3mail/web"
)

// version wird beim Bauen gesetzt: -ldflags "-X main.version=v0.2.0".
var version = "dev"

func main() {
	var (
		bucket      = flag.String("bucket", "", "S3-Bucket mit den Rohmails")
		prefix      = flag.String("prefix", "", "Wurzel-Prefix, z.B. mail/")
		region      = flag.String("region", "", "AWS-Region, z.B. eu-central-1")
		profil      = flag.String("profile", "", "AWS-Profil aus ~/.aws/credentials")
		absender    = flag.String("from", "", "Absender fuer Antworten (in SES verifiziert)")
		port        = flag.Int("port", 0, "Standard 8765")
		host        = flag.String("host", "", "Standard 127.0.0.1")
		setup       = flag.Bool("setup", false, "Assistent oeffnen, auch wenn schon konfiguriert")
		noSend      = flag.Bool("no-send", false, "SES-Versand deaktivieren (reiner Lesemodus)")
		noDelete    = flag.Bool("no-delete", false, "Endgueltiges Loeschen sperren")
		noBrowser   = flag.Bool("no-browser", false, "Browser nicht automatisch oeffnen")
		zeigVersion = flag.Bool("version", false, "Version ausgeben und beenden")
	)
	flag.Parse()

	if *zeigVersion {
		fmt.Printf("s3mail %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	// Das SDK muss die Zugangsdaten dort suchen, wo der Assistent sie hinschreibt.
	awsx.GeteiltesVerzeichnis = konfig.AWSVerzeichnis()

	k := konfig.Laden()
	setzeWenn(&k.Bucket, *bucket)
	setzeWenn(&k.Region, *region)
	setzeWenn(&k.Profil, *profil)
	setzeWenn(&k.Absender, *absender)
	setzeWenn(&k.Host, *host)
	if *prefix != "" {
		k.Prefix = konfig.PrefixNormalisieren(*prefix)
	}
	if *port != 0 {
		k.Port = *port
	}
	if *noDelete {
		k.AllowDelete = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := web.NewServer(nil, web.NeuesToken(), k.Host, k.Port, nil)

	// Der Assistent schaltet das Postfach im laufenden Prozess scharf - nach
	// "Speichern und starten" soll niemand das Programm neu starten muessen.
	ass := &assistent.Assistent{}
	ass.Aktivieren = func(neu konfig.Konfig) error {
		return scharfschalten(ctx, srv, neu, *noSend)
	}
	srv.MitAssistent(ass)

	if k.Bucket != "" && !*setup {
		if err := scharfschalten(ctx, srv, k, *noSend); err != nil {
			fmt.Printf("Verbindung fehlgeschlagen: %s\nStarte den Assistenten.\n\n",
				awsx.Klartext(err, k.Profil))
		}
	}

	adresse := net.JoinHostPort(k.Host, strconv.Itoa(k.Port))
	lauscher, err := net.Listen("tcp", adresse)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Kann nicht auf %s lauschen: %v\n", adresse, err)
		os.Exit(1)
	}
	srv.Port = lauscher.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/?t=%s", k.Host, srv.Port, srv.Token)

	if srv.Mailbox == nil {
		fmt.Printf("s3mail ist noch nicht eingerichtet - Assistent: %s\n", url)
	} else {
		fmt.Printf("s3mail laeuft auf %s   (Strg+C zum Beenden)\n", url)
		fmt.Printf("Bucket: %s/%s\n", k.Bucket, k.Prefix)
	}
	// Unter Windows startet s3mail per Doppelklick; wer das Konsolenfenster
	// schliesst, kaeme sonst nicht mehr an die Adresse heran.
	if pfad, err := web.TokenDateiSchreiben(konfig.Verzeichnis(), url); err == nil && pfad != "" {
		fmt.Printf("Adresse steht auch in: %s\n", pfad)
	}
	defer web.TokenDateiEntfernen(konfig.Verzeichnis())

	if !*noBrowser {
		go oeffneBrowser(url)
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		abschluss, abbrechen := context.WithTimeout(context.Background(), 3*time.Second)
		defer abbrechen()
		_ = httpSrv.Shutdown(abschluss)
	}()
	if err := httpSrv.Serve(lauscher); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Println("\nTschuess.")
}

// scharfschalten baut Postfach und Versand aus einer Konfiguration.
func scharfschalten(ctx context.Context, srv *web.Server, k konfig.Konfig, noSend bool) error {
	cfg, err := awsx.Sitzung(ctx, k.Profil, k.Region)
	if err != nil {
		return err
	}
	if err := awsx.ZugangPruefen(ctx, cfg); err != nil {
		return err
	}
	s3 := awsx.NeuS3(cfg, "")
	mb := store.NewMailbox(ctx, s3, awsx.NeuKMS(cfg, ""), k.Bucket, k.Prefix,
		konfig.CacheVerzeichnis(), k.AllowDelete)
	srv.Mailbox = mb
	srv.Config = map[string]any{
		"bucket": k.Bucket, "root": mb.Root, "default_from": k.Absender,
		"can_send": !noSend, "config_file": konfig.Datei(),
	}
	if !noSend {
		srv.MitVersand(awsx.NeuSES(cfg, ""), k.Absender)
	}
	return nil
}

func setzeWenn(ziel *string, wert string) {
	if wert != "" {
		*ziel = wert
	}
}

// oeffneBrowser ruft auf jeder Plattform das Richtige auf.
func oeffneBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
