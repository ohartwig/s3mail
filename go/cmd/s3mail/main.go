// s3mail - mail client for messages that Amazon SES writes into an S3 bucket
// as raw MIME.
//
// Starts a local web server and opens the mailbox in the browser. Folders are
// real S3 prefixes, the state is shared in the bucket, replies go out over SES.
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

// version is set at build time: -ldflags "-X main.version=v0.2.0".
var version = "dev"

func main() {
	// The configuration is read before the flags are defined: it carries the
	// language, and the flag descriptions are the first thing a reader sees.
	k := config.Load()
	cat := i18n.Get(k.Language)

	var (
		bucket      = flag.String("bucket", "", cat.T("cli.bucket"))
		prefix      = flag.String("prefix", "", cat.T("cli.prefix"))
		region      = flag.String("region", "", cat.T("cli.region"))
		profile     = flag.String("profile", "", cat.T("cli.profile"))
		sender      = flag.String("from", "", cat.T("cli.from"))
		port        = flag.Int("port", 0, cat.T("cli.port"))
		host        = flag.String("host", "", cat.T("cli.host"))
		setup       = flag.Bool("setup", false, cat.T("cli.setup"))
		noSend      = flag.Bool("no-send", false, cat.T("cli.noSend"))
		noDelete    = flag.Bool("no-delete", false, cat.T("cli.noDelete"))
		noBrowser   = flag.Bool("no-browser", false, cat.T("cli.noBrowser"))
		showVersion = flag.Bool("version", false, cat.T("cli.version"))
		refreshSecs = flag.Int("refresh", 60, cat.T("cli.refresh"))
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("s3mail %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	// The SDK has to look for the credentials where the wizard writes them.
	awsx.SharedDir = config.AWSDir()

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
	// The language belongs in the configuration the server hands to the page,
	// or a fresh browser gets the wizard in its own language instead of the one
	// that was chosen here.
	srv := web.NewServer(nil, web.NewToken(), k.Host, k.Port,
		map[string]any{"language": k.Language})

	// The wizard arms the mailbox inside the running process - after "save and
	// start" nobody should have to restart the program.
	wiz := &wizard.Wizard{}
	wiz.Activate = func(fresh config.Config) error {
		return activate(ctx, srv, fresh, *noSend, *refreshSecs)
	}
	srv.WithWizard(wiz)

	if k.Bucket != "" && !*setup {
		if err := activate(ctx, srv, k, *noSend, *refreshSecs); err != nil {
			// At startup there is no request and therefore no language from
			// the browser - the one from the configuration has to do.
			startErr = awsx.PlainText(err, k.Profile, cat)
		}
	}

	// Without a console window (macOS bundle, Windows GUI mode) every message goes
	// nowhere. So the start is written to a file as well.
	logFile := startLog()
	defer logFile.Close()

	address := net.JoinHostPort(k.Host, strconv.Itoa(k.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// Usually this means s3mail is already running. As a bundle without a
		// console that is the nastiest variant - the double click only reports a
		// LaunchServices timeout (-1712), because the app as an LSUIElement does
		// not answer the launch request, and the window sits somewhere behind.
		// So do not abort with an error: bring the existing window to the front
		// and withdraw.
		if url, ok := runningInstance(); ok {
			report(logFile, "%s", cat.T("cli.alreadyRunning"))
			openWindow(url)
			return
		}
		fmt.Fprintln(os.Stderr, cat.Tf("cli.cannotListen", address, err))
		os.Exit(1)
	}
	srv.Port = listener.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s:%d/?t=%s", k.Host, srv.Port, srv.Token)

	if startErr != "" {
		report(logFile, "%s", cat.Tf("cli.connectFailed", startErr))
	}
	if srv.Mailbox == nil {
		report(logFile, "%s", cat.Tf("cli.notConfigured", url))
	} else {
		report(logFile, "%s", cat.Tf("cli.running", url))
		report(logFile, "%s", cat.Tf("cli.bucketLine", k.Bucket, k.Prefix))
	}
	// On Windows s3mail starts by double click; whoever closes the console window
	// would otherwise have no way back to the address.
	if path, err := web.WriteTokenFile(config.Dir(), url, cat.T("token.file.note")); err == nil && path != "" {
		report(logFile, "%s", cat.Tf("cli.addressFile", path))
	}
	defer web.RemoveTokenFile(config.Dir())

	if !*noBrowser {
		go openWindow(url)
	}

	httpSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	// The "quit" button in the interface - without it the server would keep
	// running after the window is closed, visible to nobody.
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

// serverConfig is what the page gets to see of the configuration.
//
// The language has to be in there: the server reads it from exactly this map
// when a request carries no cookie. Without it the choice from the setup is
// stored, used for the console and then ignored by the very page it was made
// on - the browser's preference wins instead, and the setting looks broken
// while it is only unread.
func serverConfig(k config.Config, root string, noSend bool, refreshSeconds int) map[string]any {
	return map[string]any{
		"bucket": k.Bucket, "root": root, "default_from": k.From,
		"can_send": !noSend, "config_file": config.File(),
		"refresh_seconds": refreshSeconds, "language": k.Language,
	}
}

// activate builds mailbox and sending from a configuration.
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
	srv.Config = serverConfig(k, mb.Root, noSend, refreshSeconds)
	if !noSend {
		srv.WithSender(awsx.NewSES(cfg, ""), k.From)
		// The suppression list hangs off sending: whoever may not send need not be
		// able to exclude anyone from being sent to either.
		srv.WithSuppressionList(suppressions{awsx.NewSuppressions(cfg, "")})
	}
	return nil
}

func setIf(target *string, value string) {
	if value != "" {
		*target = value
	}
}
