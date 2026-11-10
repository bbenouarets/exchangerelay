package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
	_ "github.com/emersion/go-message/charset"
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
	backend  *SMTPBackend
	conn     net.Addr
	log      *slog.Logger
	from     string
	authUser string
	to       []string
}

func NewSMTPBackend(cfg *config.Config, client *graph.Client) *SMTPBackend {
	return &SMTPBackend{cfg: cfg, graph: client}
}

func (b *SMTPBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	remote := c.Conn().RemoteAddr()
	_, tls := c.TLSConnectionState()
	log := slog.With("remote", remote.String())
	log.Debug("smtp: session started", "tls", tls)
	return &SMTPSession{backend: b, conn: remote, log: log}, nil
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
		s.log.Warn("smtp: auth failed", "reason", "unsupported mechanism", "mechanism", mech)
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(connidentity, username, password string) error {
		if username == "" || password == "" {
			s.log.Warn("smtp: auth failed", "reason", "empty credentials", "user", username)
			return smtp.ErrAuthFailed
		}
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		exists, err := s.backend.graph.MailboxExists(ctx, username)
		if err != nil {
			s.log.Error("smtp: auth failed", "reason", "cannot verify mailbox", "user", username, "err", err)
			return smtp.ErrAuthFailed
		}
		if !exists {
			s.log.Warn("smtp: auth failed", "reason", "mailbox does not exist in tenant", "user", username)
			return smtp.ErrAuthFailed
		}
		s.authUser = username
		s.log.Info("smtp: auth", "user", username)
		return nil
	}), nil
}

func (s *SMTPSession) Logout() error {
	s.log.Debug("smtp: session ended")
	return nil
}

func (s *SMTPSession) Mail(from string, opts *smtp.MailOptions) error {
	fromAddr, err := mail.ParseAddress(from)
	if err != nil {
		s.log.Warn("smtp: sender rejected", "reason", "invalid from address", "from", from)
		return errors.New("invalid from address")
	}
	if !s.backend.allowedFrom(s.conn, fromAddr.Address) {
		s.log.Warn("smtp: sender rejected", "reason", "sender not allowed for this IP", "from", fromAddr.Address)
		return &smtp.SMTPError{
			Code:    550,
			Message: "relay access denied: sender not allowed for this IP",
		}
	}
	s.from = fromAddr.Address
	s.log.Debug("smtp: sender accepted", "from", s.from)
	return nil
}

func (s *SMTPSession) Rcpt(to string, opts *smtp.RcptOptions) error {
	toAddr, err := mail.ParseAddress(to)
	if err != nil {
		s.log.Warn("smtp: recipient rejected", "reason", "invalid recipient address", "to", to)
		return errors.New("invalid recipient address")
	}
	s.to = append(s.to, toAddr.Address)
	s.log.Debug("smtp: recipient accepted", "to", toAddr.Address)
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
		s.log.Warn("smtp: reading message failed", "from", s.from, "err", err)
		return err
	}

	_, _, subject, body, contentType, err := parseMessage(raw)
	if err != nil {
		s.log.Warn("smtp: message rejected", "reason", "cannot parse message", "from", s.from, "size", len(raw), "err", err)
		return errors.New("cannot parse message")
	}

	// Graph sendMail dispatches from a real tenant mailbox. Prefer the
	// mailbox that authenticated; fall back to the envelope sender, which
	// is only valid if it is a tenant mailbox itself.
	sender := s.from
	if s.authUser != "" {
		sender = s.authUser
	}

	log := s.log.With("from", s.from, "mailbox", sender, "to", strings.Join(s.to, ","), "size", len(raw))

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	err = s.backend.graph.SendMail(ctx, sender, s.to, nil, subject, body, contentType)
	if err != nil {
		log.Error("smtp: relaying message failed", "duration", time.Since(start), "err", err)
	} else {
		log.Info("smtp: message relayed", "duration", time.Since(start))
	}
	if err != nil && strings.Contains(err.Error(), "ErrorInvalidUser") {
		return &smtp.SMTPError{
			Code:    554,
			Message: fmt.Sprintf("sender mailbox %q does not exist in tenant; authenticate with a real mailbox (AUTH PLAIN) or send from an existing one", sender),
		}
	}
	return err
}

func parseMessage(raw []byte) (from string, to []string, subject, body, contentType string, err error) {
	contentType = "Text"

	msg, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil {
		return "", nil, "", "", "", err
	}

	subject, _ = msg.Header.Text("Subject")
	from, _ = msg.Header.Text("From")
	if addresses, errs := mail.ParseAddressList(msg.Header.Get("To")); errs == nil {
		for _, addr := range addresses {
			to = append(to, addr.Address)
		}
	}

	var textBuf, htmlBuf bytes.Buffer
	for {
		part, err := msg.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", nil, "", "", "", err
		}

		// Only plain text and HTML make it into the Graph body; attachments
		// and other binary parts would otherwise corrupt the text.
		switch {
		case strings.HasPrefix(part.Header.Get("Content-Type"), "text/html"):
			if _, err := io.Copy(&htmlBuf, part.Body); err != nil {
				return "", nil, "", "", "", err
			}
		case strings.HasPrefix(part.Header.Get("Content-Type"), "text/plain"):
			if _, err := io.Copy(&textBuf, part.Body); err != nil {
				return "", nil, "", "", "", err
			}
		}
	}

	if htmlBuf.Len() > 0 {
		body = htmlBuf.String()
		contentType = "HTML"
	} else {
		body = textBuf.String()
	}
	return from, to, subject, body, contentType, nil
}
