package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/bbenouarets/exchangerelay/config"
	"github.com/bbenouarets/exchangerelay/graph"
)

const requestTimeout = 60 * time.Second

type SMTPBackend struct {
	cfg   *config.Config
	graph *graph.Client
}

type SMTPSession struct {
	backend *SMTPBackend
	conn    net.Addr
	from    string
	authUser string
	to      []string
}

func NewSMTPBackend(cfg *config.Config, client *graph.Client) *SMTPBackend {
	return &SMTPBackend{cfg: cfg, graph: client}
}

func (b *SMTPBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &SMTPSession{backend: b, conn: c.Conn().RemoteAddr()}, nil
}

func (b *SMTPBackend) findSender(ip string) (config.Sender, bool) {
	for _, s := range b.cfg.AllowedSenders {
		if s.IPAddress == ip || s.IPAddress == "*" {
			return s, true
		}
	}
	return config.Sender{}, false
}

func (b *SMTPBackend) allowedFrom(addr net.Addr, from string) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	sender, ok := b.findSender(host)
	if !ok {
		return false
	}
	for _, e := range sender.Emails {
		// "*" means every e-mail address is allowed.
		if e == "*" || strings.EqualFold(e, from) {
			return true
		}
	}
	return false
}

func (s *SMTPSession) Reset() {
	s.from = ""
	s.to = nil
}

// AuthMechanisms returns the supported SASL mechanisms for SMTP AUTH.
func (s *SMTPSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}

// Auth spawns a SASL server for the given mechanism.
func (s *SMTPSession) Auth(mech string) (sasl.Server, error) {
	if mech != sasl.Plain {
		log.Printf("smtp: auth failed, unsupported mechanism %q", mech)
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(connidentity, username, password string) error {
		if username == "" || password == "" {
			log.Printf("smtp: auth failed for empty credentials (username=%q)", username)
			return smtp.ErrAuthFailed
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		exists, err := s.backend.graph.MailboxExists(ctx, username)
		if err != nil {
			log.Printf("smtp: auth failed, cannot verify mailbox %q: %v", username, err)
			return smtp.ErrAuthFailed
		}
		if !exists {
			log.Printf("smtp: auth failed, mailbox %q does not exist in tenant", username)
			return smtp.ErrAuthFailed
		}
		s.authUser = username
		return nil
	}), nil
}

func (s *SMTPSession) Logout() error {
	return nil
}

func (s *SMTPSession) Mail(from string, opts *smtp.MailOptions) error {
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		return errors.New("invalid from address")
	}
	if !s.backend.allowedFrom(s.conn, fromAddr.Address) {
		return &smtp.SMTPError{
			Code:    550,
			Message: "relay access denied: sender not allowed for this IP",
		}
	}
	s.from = fromAddr.Address
	return nil
}

func (s *SMTPSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		return errors.New("invalid recipient address")
	}
	s.to = append(s.to, toAddr.Address)
	return nil
}

func (s *SMTPSession) Data(r io.Reader) error {
	if s.from == "" {
		return errors.New("no valid sender")
	}
	if len(s.to) == 0 {
		return errors.New("no recipients")
	}

	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	subject, body, contentType, err := parseMessage(raw)
	if err != nil {
		return errors.New("cannot parse message")
	}

	// Graph sendMail dispatches from a real tenant mailbox. Prefer the
	// mailbox that authenticated; fall back to the envelope sender, which
	// is only valid if it is a tenant mailbox itself.
	sender := s.from
	if s.authUser != "" {
		sender = s.authUser
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	err = s.backend.graph.SendMail(ctx, sender, s.to, nil, subject, body, contentType)
	if err != nil && strings.Contains(err.Error(), "ErrorInvalidUser") {
		return &smtp.SMTPError{
			Code:    554,
			Message: fmt.Sprintf("sender mailbox %q does not exist in tenant; authenticate with a real mailbox (AUTH PLAIN) or send from an existing one", sender),
		}
	}
	return err
}

func parseMessage(raw []byte) (subject, body, contentType string, err error) {
	contentType = "Text"

	msg, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", "", "", err
	}

	subject, _ = msg.Header.Text("Subject")

	var textBuf, htmlBuf bytes.Buffer
	for {
		part, err := msg.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", "", err
		}
		switch part.Header.Get("Content-Type") {
		case "text/html":
			if _, err := io.Copy(&htmlBuf, part.Body); err != nil {
				return "", "", "", err
			}
		default:
			if _, err := io.Copy(&textBuf, part.Body); err != nil {
				return "", "", "", err
			}
		}
	}

	if htmlBuf.Len() > 0 {
		body = htmlBuf.String()
		contentType = "HTML"
	} else {
		body = textBuf.String()
	}
	return subject, body, contentType, nil
}
