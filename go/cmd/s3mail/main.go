// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

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

	"git.ole-hartwig.eu/development/s3mail/s3mail/go/awsx"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/config"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/demo"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/i18n"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/keyring"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/s3fake"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/store"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/web"
	"git.ole-hartwig.eu/development/s3mail/s3mail/go/wizard"
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
		noCache     = flag.Bool("no-cache", false, cat.T("cli.noCache"))
		debug       = flag.Bool("debug", false, cat.T("cli.debug"))
		plainCache  = flag.Bool("cache-plaintext", false, cat.T("cli.cachePlaintext"))
		showVersion = flag.Bool("version", false, cat.T("cli.version"))
		refreshSecs = flag.Int("refresh", 60, cat.T("cli.refresh"))
		mcpMode     = flag.Bool("mcp", false, cat.T("cli.mcp"))
		mcpReadOnly = flag.Bool("mcp-readonly", false, cat.T("cli.mcpReadonly"))
		demoMode    = flag.Bool("demo", false, cat.T("cli.demo"))
	)
	flag.Parse()

	// One place decides whether anything is written to disk, so that no caller
	// can forget the switch. Everything that would land there hangs off this one
	// string - index, message bodies and the local copy of the state - and
	// store.NewMailbox writes nothing at all when it is empty.
	cacheDir := func() string {
		if *noCache {
			return ""
		}
		return config.CacheDir()
	}

	// The key the cache is encrypted with. Without a keyring there is nowhere to
	// keep it, and rather than quietly writing plain text - which would undo the
	// bucket's own encryption for every message ever opened - s3mail stops and
	// asks for a decision. See keyring/keyring.go.
	// --demo is exempt: it writes nothing to disk in the first place, so there
	// is nothing to encrypt and no reason to demand a keyring. Somebody who
	// wants to see what this looks like should not be stopped by a decision
	// about a cache that will never be written.
	var cacheKey []byte
	if !*noCache && !*plainCache && !*demoMode {
		key, err := keyring.Key()
		if err != nil {
			fmt.Fprintln(os.Stderr, cat.T("cli.noKeyring"))
			os.Exit(1)
		}
		cacheKey = key
	}

	if *showVersion {
		fmt.Printf("s3mail %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return
	}

	// The SDK has to look for the credentials where the wizard writes them.
	awsx.SharedDir = config.AWSDir()

	// --debug writes which call was made and what came back. Deliberately not
	// the SDK's own request logging: that would put the mail itself into a file
	// at the moment somebody is debugging and least expects it. See
	// awsx/debug.go.
	if *debug {
		awsx.SetDebug(os.Stderr)
	}

	// As an MCP server there is no window and no web server: the program is a
	// tool in somebody else's hands, and stdin and stdout are the whole
	// interface. Everything below - port, browser, token - is beside the point.
	if *mcpMode || *mcpReadOnly {
		if err := serveMCP(context.Background(), k, *mcpReadOnly, cacheDir(), cacheKey); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// The switches address the first mailbox - that is where the single one used
	// to be, and a flag cannot say which of several it means.
	if k.Accounts == nil && (*bucket != "" || *prefix != "") {
		k.Accounts = []config.Account{config.DefaultAccount()}
	}
	if len(k.Accounts) > 0 {
		a := &k.Accounts[0]
		setIf(&a.Bucket, *bucket)
		setIf(&a.Region, *region)
		setIf(&a.Profile, *profile)
		setIf(&a.From, *sender)
		if *prefix != "" {
			a.Prefix = config.NormalizePrefix(*prefix)
		}
	}
	setIf(&k.Host, *host)
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
		return activate(ctx, srv, fresh, *noSend, *refreshSecs, cacheDir(), cacheKey)
	}
	srv.WithWizard(wiz)

	if *demoMode {
		// The sample mail is written at startup, before any browser has said
		// what it reads - so without this a German machine gets a German
		// interface around English mail. The configuration still wins where it
		// has an answer; the environment only fills the gap.
		if k.Language == "" {
			k.Language = i18n.FromEnv()
		}
		if err := activateDemo(ctx, srv, k, *refreshSecs); err != nil {
			startErr = err.Error()
		}
	} else if len(k.Accounts) > 0 && !*setup {
		if err := activate(ctx, srv, k, *noSend, *refreshSecs, cacheDir(), cacheKey); err != nil {
			// At startup there is no request and therefore no language from
			// the browser - the one from the configuration has to do.
			startErr = awsx.PlainText(err, k.First().Profile, cat)
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
	if len(srv.Accounts()) == 0 {
		report(logFile, "%s", cat.Tf("cli.notConfigured", url))
	} else {
		report(logFile, "%s", cat.Tf("cli.running", url))
		for _, a := range srv.Accounts() {
			report(logFile, "%s", cat.Tf("cli.mailboxLine", a.Name))
		}
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
// serverConfig is what the page gets to see of the configuration. Only what
// applies to every mailbox - the rest rides with each answer, because it
// changes when the reader switches mailbox.
func serverConfig(k config.Config, refreshSeconds int) map[string]any {
	return map[string]any{
		"config_file": config.File(), "refresh_seconds": refreshSeconds,
		"language": k.Language,
	}
}

// activateDemo puts the sample mailbox up: the same client over a bucket that
// does not exist.
//
// It is here so somebody can see what this is before they own an AWS account -
// the same reason the iOS app carries one. And it is where the screenshots come
// from, which is the second reason: nobody should have to photograph real
// correspondence to show what a mail client looks like.
//
// Nothing is written to disk (no cache directory) and nothing can be sent (no
// sender on the account, so the compose button never appears). A sample that
// left traces or offered a button that cannot work would be worse than none.
func activateDemo(ctx context.Context, srv *web.Server, k config.Config,
	refreshSeconds int) error {
	objects, err := demo.Objects(k.Language, time.Now())
	if err != nil {
		return err
	}
	fake := s3fake.New()
	for key, body := range objects {
		fake.Objs[key] = body
	}
	// allowDelete is false, as it is everywhere the mail is not the reader's to
	// destroy: the trash is a folder.
	mb := store.NewMailbox(ctx, fake, nil, "demo", demo.Root, "", nil, false)
	if _, err := mb.Refresh(ctx); err != nil {
		return err
	}
	srv.SetAccounts([]web.Account{{
		ID: "demo", Name: i18n.Get(k.Language).T("demo.mailbox"), Mailbox: mb,
	}})
	srv.Config = serverConfig(k, refreshSeconds)
	return nil
}

// activate builds mailbox and sending from a configuration.
// activate builds a mailbox per account and hands them to the server.
//
// A mailbox that cannot be armed does not stop the others: whoever has two and
// mistypes the profile of the second should still get at the first. The error
// comes back so the start line can name it, but the program keeps running as
// long as anything came up.
func activate(ctx context.Context, srv *web.Server, k config.Config, noSend bool,
	refreshSeconds int, cacheDir string, cacheKey []byte) error {
	accounts := make([]web.Account, 0, len(k.Accounts))
	var firstErr error
	for _, a := range k.Accounts {
		cfg, err := awsx.Session(ctx, a.Profile, a.Region)
		if err == nil {
			err = awsx.CheckAccess(ctx, cfg)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		mb := store.NewMailbox(ctx, awsx.NewS3Client(cfg, ""), awsx.NewKMS(cfg, ""),
			a.Bucket, a.Prefix, cacheDir, cacheKey, k.AllowDelete)
		acc := web.Account{ID: a.ID(), Name: a.Name(), Mailbox: mb, Profile: a.Profile,
			From: a.From, Signature: a.Signature, Snippets: a.Snippets}
		if !noSend {
			acc.Sender = awsx.NewSES(cfg, "")
			// The suppression list hangs off sending: whoever may not send need not
			// be able to exclude anyone from being sent to either.
			acc.Blocked = suppressions{awsx.NewSuppressions(cfg, "")}
		}
		accounts = append(accounts, acc)

		// Push, where the mailbox has a queue. Without one the timer in the page
		// does it, exactly as before - a mailbox whose infrastructure predates
		// this must keep working.
		if a.Queue != "" {
			q := awsx.NewQueue(cfg, a.Queue)
			id := acc.ID
			go watchQueue(ctx, srv, id, q, func(c context.Context) error {
				_, err := mb.Refresh(c)
				return err
			})
		}
	}
	srv.SetAccounts(accounts)
	srv.Config = serverConfig(k, refreshSeconds)
	// A send that was started and never finished: whatever can be finished
	// without asking is finished here, before the window opens. What cannot
	// waits as a question at the top of the mailbox - see store/sending.go.
	srv.RecoverSends(ctx)
	if len(accounts) == 0 && firstErr == nil {
		firstErr = config.ErrNoBucket
	}
	return firstErr
}

func setIf(target *string, value string) {
	if value != "" {
		*target = value
	}
}
