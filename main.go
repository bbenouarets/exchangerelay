package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	gonimapsrv "github.com/emersion/go-imap/server"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/bbenouarets/exchangerelay/config"
	"github.com/bbenouarets/exchangerelay/graph"
	"github.com/bbenouarets/exchangerelay/logging"
	"github.com/bbenouarets/exchangerelay/server"
)

func fatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func main() {
	logging.Setup(slog.LevelInfo)

	cfgProv := config.NewConfigProvider("config.ini")
	cfg, err := cfgProv.Load()
	if err != nil {
		fatal("cannot load config", "path", cfgProv.Path, "err", err)
	}

	level, levelErr := logging.ParseLevel(cfg.Log.Level)
	logging.Setup(level)
	if levelErr != nil {
		slog.Warn("invalid [log] level, falling back to info", "err", levelErr)
	}

	client := graph.NewClient(cfg.Exchange.TenantID, cfg.Exchange.ClientID, cfg.Exchange.ClientSecret, "")

	if len(os.Args) > 1 && os.Args[1] == "list-mailboxes" {
		mailboxes, err := client.ListMailboxes(context.Background())
		if err != nil {
			fatal("cannot list mailboxes", "err", err)
		}
		for _, mbox := range mailboxes {
			fmt.Printf("%s | %s | %s\n", mbox.DisplayName, mbox.UserPrincipalName, mbox.Mail)
		}
		return
	}

	slog.Info("exchangerelay starting", "log_level", level.String())
	if len(cfg.AllowedSenders) == 0 {
		slog.Warn("no [host:...] sections configured, every SMTP sender will be rejected")
	}
	for _, sender := range cfg.AllowedSenders {
		slog.Info("allowed sender", "host", sender.IPAddress, "emails", strings.Join(sender.Emails, ","))
	}

	tlsConfig, err := server.SelfSignedTLS()
	if err != nil {
		fatal("cannot create TLS certificate", "err", err)
	}

	// Errors the protocol libraries report themselves (accept errors, panics
	// in a connection handler, ...) end up in the same log.
	libErrors := func(proto string) *slogPrinter {
		return &slogPrinter{log: slog.With("proto", proto)}
	}

	newSMTP := func(proto string) *gosmtp.Server {
		srv := gosmtp.NewServer(server.NewSMTPBackend(cfg, client))
		srv.Domain = "localhost"
		srv.TLSConfig = tlsConfig
		srv.AllowInsecureAuth = true
		srv.ErrorLog = libErrors(proto)
		return srv
	}
	newIMAP := func(proto string) *gonimapsrv.Server {
		srv := gonimapsrv.New(server.NewIMAPBackend(cfg, client))
		srv.TLSConfig = tlsConfig
		srv.AllowInsecureAuth = true
		srv.ErrorLog = libErrors(proto)
		srv.Enable(server.NamespaceExtension{})
		return srv
	}

	// serve binds addr and runs srv on it in the background. It reports
	// whether the listener came up; a later Serve error is sent to stopped.
	stopped := make(chan error, 1)
	serve := func(proto, addr string, srv interface{ Serve(net.Listener) error }, required bool) {
		l, err := server.Listen(proto, addr)
		if err != nil {
			if required {
				fatal("cannot listen", "proto", proto, "addr", addr, "err", err)
			}
			slog.Error("cannot listen, continuing without this listener", "proto", proto, "addr", addr, "err", err)
			return
		}
		slog.Info("listening", "proto", proto, "addr", l.Addr().String())
		go func() {
			err := srv.Serve(l)
			slog.Error("server stopped", "proto", proto, "addr", addr, "err", err)
			if required {
				stopped <- err
			}
		}()
	}

	serve("smtp", cfg.Server.SmtpAddr, newSMTP("smtp"), false)
	// Optional additional listeners for the SMTPS/IMAPS ports, if configured.
	if cfg.Server.SmtpTlsAddr != "" {
		serve("smtps", cfg.Server.SmtpTlsAddr, newSMTP("smtps"), false)
	}
	if cfg.Server.ImapTlsAddr != "" {
		serve("imaps", cfg.Server.ImapTlsAddr, newIMAP("imaps"), false)
	}
	serve("imap", cfg.Server.ImapAddr, newIMAP("imap"), true)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-signals:
		slog.Info("shutting down", "signal", sig.String())
	case <-stopped:
		os.Exit(1)
	}
}

// slogPrinter adapts slog to the Printf/Println logger interface that go-smtp
// and go-imap use for their internal error log.
type slogPrinter struct {
	log *slog.Logger
}

func (p *slogPrinter) Printf(format string, v ...interface{}) {
	p.log.Error(strings.TrimSpace(fmt.Sprintf(format, v...)))
}

func (p *slogPrinter) Println(v ...interface{}) {
	p.log.Error(strings.TrimSpace(fmt.Sprintln(v...)))
}
