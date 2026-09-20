package main

import (
	"context"
	"fmt"
	"log"
	"os"

	gosmtp "github.com/emersion/go-smtp"
	gonimapsrv "github.com/emersion/go-imap/server"

	"github.com/bbenouarets/exchangerelay/config"
	"github.com/bbenouarets/exchangerelay/graph"
	"github.com/bbenouarets/exchangerelay/server"
)

func main() {
	cfgProv := config.NewConfigProvider("config.ini")
	cfg, err := cfgProv.Load()
	if err != nil {
		panic(err)
	}

	client := graph.NewClient(cfg.Exchange.TenantID, cfg.Exchange.ClientID, cfg.Exchange.ClientSecret, "")

	if len(os.Args) > 1 && os.Args[1] == "list-mailboxes" {
		mailboxes, err := client.ListMailboxes(context.Background())
		if err != nil {
			panic(err)
		}
		for _, mbox := range mailboxes {
			fmt.Printf("%s | %s | %s\n", mbox.DisplayName, mbox.UserPrincipalName, mbox.Mail)
		}
		return
	}

	for _, sender := range cfg.AllowedSenders {
		fmt.Printf("%s: %v\n", sender.IPAddress, sender.Emails)
	}
	fmt.Printf("SMTP listening on %s\n", cfg.Server.SmtpAddr)
	fmt.Printf("IMAP listening on %s\n", cfg.Server.ImapAddr)

	tlsConfig, err := server.SelfSignedTLS()
	if err != nil {
		panic(err)
	}

	smtpSrv := gosmtp.NewServer(server.NewSMTPBackend(cfg, client))
	smtpSrv.Addr = cfg.Server.SmtpAddr
	smtpSrv.Domain = "localhost"
	smtpSrv.TLSConfig = tlsConfig
	smtpSrv.AllowInsecureAuth = true

	imapSrv := gonimapsrv.New(server.NewIMAPBackend(cfg, client))
	imapSrv.Addr = cfg.Server.ImapAddr
	imapSrv.TLSConfig = tlsConfig
	imapSrv.AllowInsecureAuth = true
	imapSrv.Enable(server.NamespaceExtension{})

	go func() {
		if err := smtpSrv.ListenAndServe(); err != nil {
			log.Printf("SMTP server error: %v", err)
		}
	}()

	if err := imapSrv.ListenAndServe(); err != nil {
		log.Fatalf("IMAP server error: %v", err)
	}
}
