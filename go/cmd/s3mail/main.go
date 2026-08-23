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
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"s3mail/awsx"
	"s3mail/config"
	"s3mail/i18n"
	"s3mail/store"
	"s3mail/web"
	"s3mail/wizard"
)

// version wird beim Bauen gesetzt: -ldflags "-X main.version=v0.2.0".
var version = "dev"

func main() {
	var (
		bucket      = flag.String("bucket", "", "S3-Bucket mit den Rohmails")
		prefix      = flag.String("prefix", "", "Wurzel-Prefix, z.B. mail/")
		region      = flag.String("region", "", "AWS-Region, z.B. eu-central-1")
		profile     = flag.String("profile", "", "AWS-Profil aus ~/.aws/credentials")
		sender      = flag.String("from", "", "Absender fuer Antworten (in SES verifiziert)")
		port        = flag.Int("port", 0, "Standard 8765")
		host        = flag.String("host", "", "Standard 127.0.0.1")
		setup       = flag.Bool("setup", false, "Assistent oeffnen, auch wenn schon konfiguriert")
		noSend      = flag.Bool("no-send", false, "SES-Versand deaktivieren (reiner Lesemodus)")
		noDelete    = flag.Bool("no-delete", false, "Endgueltiges Loeschen sperren")
		noBrowser   = flag.Bool("no-browser", false, "Browser nicht automatisch oeffnen")
		showVersion = flag.Bool("version", false, "Version ausgeben und beenden")
		refreshSecs = flag.Int("refresh", 60, "Sekunden zwischen automatischen Abgleichen; 0 schaltet ab")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("s3mail %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	// Das SDK muss die Zugangsdaten dort suchen, wo der Assistent sie hinschreibt.
	awsx.SharedDir = config.AWSDir()

	k := config.Load()
	setIf(&k.Bucket, *bucket)
	setIf(&k.Region, *region)
	setIf(&k.Profile, *profile)
	setIf(&k.From, *sender)
	setIf(&k.Host, *host)
	if *prefix != "" {
		k.Prefix = config.NormalizePrefix(*prefix)
	}
	if *port != 0 {
		k.Port = *port
	}
	if *noDelete {
		k.AllowDelete = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var startErr string
	srv := web.NewServer(nil, web.NewToken(), k.Host, k.Port, nil)

	// Der Assistent schaltet das Postfach im laufenden Prozess scharf - nach
	// "Speichern und starten" soll niemand das Programm neu starten muessen.
	ass := &wizard.Wizard{}
	ass.Activate = func(fresh config.Config) error {
		return activate(ctx, srv, fresh, *noSend, *refreshSecs)
	}
	srv.WithWizard(ass)

	if k.Bucket != "" && !*setup {
		if err := activate(ctx, srv, k, *noSend, *refreshSecs); err != nil {
			// Beim Start gibt es keine Anfrage und damit keine Sprache aus
			// dem Browser - die aus der Konfiguration muss genuegen.
			startErr = awsx.PlainText(err, k.Profile, i18n.Get(k.Language))
		}
	}

	// Ohne Konsolenfenster (macOS-Bundle, Windows-GUI-Modus) geht jede Meldung ins
	// Nichts. Deshalb landet der Start zusaetzlich in einer Datei.
	logFile := startLog()
	defer logFile.Close()

	address := net.JoinHostPort(k.Host, strconv.Itoa(k.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// Meistens heisst das: s3mail laeuft schon. Als Bundle ohne Konsole ist
		// das die unangenehmste Variante - der Doppelklick meldet nur einen
		// LaunchServices-Timeout (-1712), weil die App als LSUIElement nicht auf
		// den Start-Request antwortet, und das Fenster steht irgendwo hinten.
		// Also nicht mit einem Fehler abbrechen, sondern das vorhandene Fenster
		// nach vorne holen und sich zurueckziehen.
		if url, ok := runningInstance(); ok {
			report(logFile, "s3mail laeuft bereits - hole das Fenster nach vorne.")
			openWindow(url)
			return
		}
		fmt.Fprintf(os.Stderr, "Kann nicht auf %s lauschen: %v\n", address, err)
		os.Exit(1)
	}
	srv.Port = listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/?t=%s", k.Host, srv.Port, srv.Token)

	if startErr != "" {
		report(logFile, "Verbindung fehlgeschlagen: %s", startErr)
	}
	if srv.Mailbox == nil {
		report(logFile, "s3mail ist noch nicht eingerichtet - Assistent: %s", url)
	} else {
		report(logFile, "s3mail laeuft auf %s   (Strg+C zum Beenden)", url)
		report(logFile, "Bucket: %s/%s", k.Bucket, k.Prefix)
	}
	// Unter Windows startet s3mail per Doppelklick; wer das Konsolenfenster
	// schliesst, kaeme sonst nicht mehr an die Adresse heran.
	if path, err := web.WriteTokenFile(config.Dir(), url); err == nil && path != "" {
		report(logFile, "Adresse steht auch in: %s", path)
	}
	defer web.RemoveTokenFile(config.Dir())

	if !*noBrowser {
		go openWindow(url)
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	// Der Knopf "Beenden" in der Oberflaeche - ohne den liefe der Server nach dem
	// Schliessen des Fensters weiter, sichtbar fuer niemanden.
	srv.OnShutdown = stop
	go func() {
		<-ctx.Done()
		finish, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(finish)
	}()
	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
	}
	fmt.Println("\nTschuess.")
}

// activate baut Postfach und Versand aus einer Konfiguration.
func activate(ctx context.Context, srv *web.Server, k config.Config, noSend bool, refreshSeconds int) error {
	cfg, err := awsx.Session(ctx, k.Profile, k.Region)
	if err != nil {
		return err
	}
	if err := awsx.CheckAccess(ctx, cfg); err != nil {
		return err
	}
	s3 := awsx.NewS3(cfg, "")
	mb := store.NewMailbox(ctx, s3, awsx.NewKMS(cfg, ""), k.Bucket, k.Prefix,
		config.CacheDir(), k.AllowDelete)
	srv.Mailbox = mb
	srv.Config = map[string]any{
		"bucket": k.Bucket, "root": mb.Root, "default_from": k.From,
		"can_send": !noSend, "config_file": config.File(),
		"refresh_seconds": refreshSeconds,
	}
	if !noSend {
		srv.WithSender(awsx.NewSES(cfg, ""), k.From)
		// Die Sperrliste haengt am Versand: wer nicht senden darf, muss auch
		// niemanden vom Senden ausschliessen koennen.
		srv.WithSuppressionList(suppressions{awsx.NewSuppressions(cfg, "")})
	}
	return nil
}

func setIf(target *string, value string) {
	if value != "" {
		*target = value
	}
}
