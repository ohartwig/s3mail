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
		refreshSek  = flag.Int("refresh", 60, "Sekunden zwischen automatischen Abgleichen; 0 schaltet ab")
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

	var startfehler string
	srv := web.NewServer(nil, web.NeuesToken(), k.Host, k.Port, nil)

	// Der Assistent schaltet das Postfach im laufenden Prozess scharf - nach
	// "Speichern und starten" soll niemand das Programm neu starten muessen.
	ass := &assistent.Assistent{}
	ass.Aktivieren = func(neu konfig.Konfig) error {
		return scharfschalten(ctx, srv, neu, *noSend, *refreshSek)
	}
	srv.MitAssistent(ass)

	if k.Bucket != "" && !*setup {
		if err := scharfschalten(ctx, srv, k, *noSend, *refreshSek); err != nil {
			startfehler = awsx.Klartext(err, k.Profil)
		}
	}

	// Ohne Konsolenfenster (macOS-Bundle, Windows-GUI-Modus) geht jede Meldung ins
	// Nichts. Deshalb landet der Start zusaetzlich in einer Datei.
	protokoll := starteProtokoll()
	defer protokoll.Close()

	adresse := net.JoinHostPort(k.Host, strconv.Itoa(k.Port))
	lauscher, err := net.Listen("tcp", adresse)
	if err != nil {
		// Meistens heisst das: s3mail laeuft schon. Als Bundle ohne Konsole ist
		// das die unangenehmste Variante - der Doppelklick meldet nur einen
		// LaunchServices-Timeout (-1712), weil die App als LSUIElement nicht auf
		// den Start-Request antwortet, und das Fenster steht irgendwo hinten.
		// Also nicht mit einem Fehler abbrechen, sondern das vorhandene Fenster
		// nach vorne holen und sich zurueckziehen.
		if url, ok := laufendeInstanz(); ok {
			melde(protokoll, "s3mail laeuft bereits - hole das Fenster nach vorne.")
			oeffneFenster(url)
			return
		}
		fmt.Fprintf(os.Stderr, "Kann nicht auf %s lauschen: %v\n", adresse, err)
		os.Exit(1)
	}
	srv.Port = lauscher.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/?t=%s", k.Host, srv.Port, srv.Token)

	if startfehler != "" {
		melde(protokoll, "Verbindung fehlgeschlagen: %s", startfehler)
	}
	if srv.Mailbox == nil {
		melde(protokoll, "s3mail ist noch nicht eingerichtet - Assistent: %s", url)
	} else {
		melde(protokoll, "s3mail laeuft auf %s   (Strg+C zum Beenden)", url)
		melde(protokoll, "Bucket: %s/%s", k.Bucket, k.Prefix)
	}
	// Unter Windows startet s3mail per Doppelklick; wer das Konsolenfenster
	// schliesst, kaeme sonst nicht mehr an die Adresse heran.
	if pfad, err := web.TokenDateiSchreiben(konfig.Verzeichnis(), url); err == nil && pfad != "" {
		melde(protokoll, "Adresse steht auch in: %s", pfad)
	}
	defer web.TokenDateiEntfernen(konfig.Verzeichnis())

	if !*noBrowser {
		go oeffneFenster(url)
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	// Der Knopf "Beenden" in der Oberflaeche - ohne den liefe der Server nach dem
	// Schliessen des Fensters weiter, sichtbar fuer niemanden.
	srv.BeimBeenden = stop
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
func scharfschalten(ctx context.Context, srv *web.Server, k konfig.Konfig, noSend bool, refreshSekunden int) error {
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
		"refresh_seconds": refreshSekunden,
	}
	if !noSend {
		srv.MitVersand(awsx.NeuSES(cfg, ""), k.Absender)
		// Die Sperrliste haengt am Versand: wer nicht senden darf, muss auch
		// niemanden vom Senden ausschliessen koennen.
		srv.MitSperrliste(sperrliste{awsx.NeueSperrliste(cfg, "")})
	}
	return nil
}

func setzeWenn(ziel *string, wert string) {
	if wert != "" {
		*ziel = wert
	}
}
