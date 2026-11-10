package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	gomessage "github.com/emersion/go-message"
	gotextproto "github.com/emersion/go-message/textproto"

	"github.com/bbenouarets/exchangerelay/config"
	"github.com/bbenouarets/exchangerelay/graph"
)

const imapMaxMessages = 50

type IMAPBackend struct {
	cfg   *config.Config
	graph *graph.Client
}

func NewIMAPBackend(cfg *config.Config, client *graph.Client) *IMAPBackend {
	return &IMAPBackend{cfg: cfg, graph: client}
}

func (b *IMAPBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	log := slog.With("user", username)
	tls := false
	if connInfo != nil {
		if connInfo.RemoteAddr != nil {
			log = slog.With("remote", connInfo.RemoteAddr.String(), "user", username)
		}
		tls = connInfo.TLS != nil
	}

	if password == "" {
		log.Warn("imap: login failed", "reason", "empty password")
		return nil, backend.ErrInvalidCredentials
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	exists, err := b.graph.MailboxExists(ctx, username)
	if err != nil {
		log.Error("imap: login failed", "reason", "cannot verify mailbox", "err", err)
		return nil, err
	}
	if !exists {
		log.Warn("imap: login failed", "reason", "mailbox does not exist in tenant")
		return nil, backend.ErrInvalidCredentials
	}

	log.Info("imap: login", "tls", tls)
	return &IMAPUser{backend: b, username: username, log: log}, nil
}

type IMAPUser struct {
	backend  *IMAPBackend
	username string
	log      *slog.Logger
}

func (u *IMAPUser) Username() string { return u.username }

func (u *IMAPUser) folders() ([]graph.Folder, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	folders, err := u.backend.graph.ListFolders(ctx, u.username)
	if err != nil {
		u.log.Error("imap: listing folders failed", "err", err)
	}
	return folders, err
}

func (u *IMAPUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	folders, err := u.folders()
	if err != nil {
		return nil, err
	}
	u.log.Info("imap: folders listed", "folders", len(folders))

	var out []backend.Mailbox
	for _, f := range folders {
		out = append(out, &IMAPMailbox{user: u, folder: wellKnownFor(f), label: f.DisplayName})
	}
	return out, nil
}

// wellKnownFor returns the Graph well-known folder name when possible,
// otherwise the actual folder ID is used for subsequent requests.
func wellKnownFor(f graph.Folder) string {
	for _, well := range graph.WellKnownFolders {
		if strings.EqualFold(well, f.DisplayName) {
			return well
		}
	}
	switch strings.ToLower(f.DisplayName) {
	case "inbox":
		return "inbox"
	}
	return f.ID
}

func (u *IMAPUser) GetMailbox(name string) (backend.Mailbox, error) {
	if name == "INBOX" {
		return &IMAPMailbox{user: u, folder: "inbox", label: "INBOX"}, nil
	}
	if well, ok := graph.WellKnownFolders[strings.ToUpper(name)]; ok {
		return &IMAPMailbox{user: u, folder: well, label: name}, nil
	}

	folders, err := u.folders()
	if err != nil {
		return nil, err
	}
	for _, f := range folders {
		if strings.EqualFold(f.DisplayName, name) {
			return &IMAPMailbox{user: u, folder: wellKnownFor(f), label: f.DisplayName}, nil
		}
	}
	u.log.Debug("imap: no such mailbox", "folder", name)
	return nil, backend.ErrNoSuchMailbox
}

func (u *IMAPUser) CreateMailbox(name string) error {
	return errors.New("Mailboxes are managed by Exchange")
}

func (u *IMAPUser) DeleteMailbox(name string) error {
	return errors.New("Mailboxes are managed by Exchange")
}

func (u *IMAPUser) RenameMailbox(oldName, newName string) error {
	return errors.New("Mailboxes are managed by Exchange")
}

func (u *IMAPUser) Logout() error {
	u.log.Info("imap: logout")
	return nil
}

func (u *IMAPUser) newMailbox() *IMAPMailbox {
	return &IMAPMailbox{user: u}
}

// IMAPMailbox implements backend.Mailbox for a single Graph mail folder.
type IMAPMailbox struct {
	user   *IMAPUser
	folder string // graph folder id / well-known name ("inbox" for INBOX)
	label  string // IMAP-facing mailbox name
}

func (m *IMAPMailbox) Name() string { return m.label }

func (m *IMAPMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{
		Attributes: []string{},
		Delimiter:  "/",
		Name:       m.label,
	}, nil
}

// log returns the session logger tagged with this mailbox's folder name.
func (m *IMAPMailbox) log() *slog.Logger {
	return m.user.log.With("folder", m.label)
}

func (m *IMAPMailbox) list() ([]graph.Mail, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	mails, err := m.user.backend.graph.ListFolderMessages(ctx, m.user.username, m.folder, imapMaxMessages)
	if err != nil {
		m.log().Error("imap: listing messages failed", "duration", time.Since(start), "err", err)
		return nil, err
	}
	m.log().Debug("imap: messages listed", "messages", len(mails), "duration", time.Since(start))
	return mails, nil
}

func (m *IMAPMailbox) raw(mailID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	raw, err := m.user.backend.graph.GetMessageRaw(ctx, m.user.username, mailID)
	if err != nil {
		m.log().Error("imap: downloading message failed", "message_id", mailID, "err", err)
	}
	return raw, err
}

func (m *IMAPMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	mails, err := m.list()
	if err != nil {
		return nil, err
	}

	status := imap.NewMailboxStatus(m.label, items)
	status.Flags = []string{}
	status.PermanentFlags = []string{imap.SeenFlag, "*"}
	status.UnseenSeqNum = 0

	var unseen uint32
	for i, msg := range mails {
		if !msg.IsRead {
			if status.UnseenSeqNum == 0 {
				status.UnseenSeqNum = uint32(i + 1)
			}
			unseen++
		}
	}

	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			status.Messages = uint32(len(mails))
		case imap.StatusRecent:
			status.Recent = 0
		case imap.StatusUnseen:
			status.Unseen = unseen
		case imap.StatusUidNext:
			status.UidNext = uint32(len(mails) + 1)
		case imap.StatusUidValidity:
			status.UidValidity = 1
		}
	}
	m.log().Info("imap: mailbox status", "messages", len(mails), "unseen", unseen)
	return status, nil
}

func (m *IMAPMailbox) SetSubscribed(subscribed bool) error { return nil }
func (m *IMAPMailbox) Check() error                        { return nil }

// graphMessage wraps a Graph mail with its raw MIME body.
type graphMessage struct {
	mail graph.Mail
	raw  []byte
	uid  uint32
}

func (g *graphMessage) flags() []string {
	if g.mail.IsRead {
		return []string{imap.SeenFlag}
	}
	return []string{}
}

func (g *graphMessage) headerAndBody() (gotextproto.Header, *bufio.Reader, error) {
	body := bufio.NewReader(bytes.NewReader(g.raw))
	hdr, err := gotextproto.ReadHeader(body)
	if err != nil {
		return gotextproto.Header{}, body, err
	}
	return hdr, body, err
}

func (g *graphMessage) entity() (*gomessage.Entity, error) {
	return gomessage.Read(bytes.NewReader(g.raw))
}

func (g *graphMessage) fetch(seqNum uint32, items []imap.FetchItem) (*imap.Message, error) {
	fetched := imap.NewMessage(seqNum, items)
	for _, item := range items {
		switch item {
		case imap.FetchEnvelope:
			hdr, _, err := g.headerAndBody()
			if err != nil {
				return nil, err
			}
			fetched.Envelope, _ = backendutil.FetchEnvelope(hdr)
		case imap.FetchBody, imap.FetchBodyStructure:
			hdr, body, err := g.headerAndBody()
			if err != nil {
				return nil, err
			}
			fetched.BodyStructure, _ = backendutil.FetchBodyStructure(hdr, body, item == imap.FetchBodyStructure)
		case imap.FetchFlags:
			fetched.Flags = g.flags()
		case imap.FetchInternalDate:
			fetched.InternalDate = g.mail.Received
		case imap.FetchRFC822Size:
			fetched.Size = uint32(len(g.raw))
		case imap.FetchUid:
			fetched.Uid = g.uid
		default:
			section, err := imap.ParseBodySectionName(item)
			if err != nil {
				break
			}
			body := bufio.NewReader(bytes.NewReader(g.raw))
			hdr, err := gotextproto.ReadHeader(body)
			if err != nil {
				return nil, err
			}
			l, _ := backendutil.FetchBodySection(hdr, body, section)
			fetched.Body[section] = l
		}
	}
	return fetched, nil
}

// collect returns the messages matching seqset, together with their sequence
// number and raw MIME body.
func (m *IMAPMailbox) collect(uid bool, seqset *imap.SeqSet) ([]*graphMessage, error) {
	mails, err := m.list()
	if err != nil {
		return nil, err
	}

	var out []*graphMessage
	for i, mail := range mails {
		// UID and sequence number are identical within a session snapshot.
		seqNum := uint32(i + 1)
		if !seqset.Contains(seqNum) {
			continue
		}

		raw, err := m.raw(mail.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, &graphMessage{mail: mail, raw: raw, uid: seqNum})
	}
	return out, nil
}

func (m *IMAPMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	start := time.Now()
	messages, err := m.collect(uid, seqset)
	if err != nil {
		return err
	}

	var sent, size int
	for i := range messages {
		seqNum := messages[i].uid
		msg, err := messages[i].fetch(seqNum, items)
		if err != nil {
			m.log().Warn("imap: skipping unparsable message", "seq", seqNum, "message_id", messages[i].mail.ID, "err", err)
			continue
		}
		ch <- msg
		sent++
		size += len(messages[i].raw)
	}
	m.log().Info("imap: messages fetched", "set", seqset.String(), "messages", sent, "bytes", size, "duration", time.Since(start))
	return nil
}

func (m *IMAPMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	mails, err := m.list()
	if err != nil {
		return nil, err
	}

	start := time.Now()
	var ids []uint32
	for i, mail := range mails {
		seqNum := uint32(i + 1)

		raw, err := m.raw(mail.ID)
		if err != nil {
			continue
		}
		gmsg := &graphMessage{mail: mail, raw: raw, uid: seqNum}

		e, err := gmsg.entity()
		if err != nil {
			continue
		}

		ok, err := backendutil.Match(e, seqNum, gmsg.uid, mail.Received, gmsg.flags(), criteria)
		if err != nil || !ok {
			continue
		}
		ids = append(ids, seqNum)
	}
	m.log().Info("imap: messages searched", "scanned", len(mails), "matches", len(ids), "duration", time.Since(start))
	return ids, nil
}

func flagFromRead(read bool) []string {
	if read {
		return []string{imap.SeenFlag}
	}
	return []string{}
}

func emptyEntity() (*gomessage.Entity, error) {
	return gomessage.New(gomessage.Header{}, bytes.NewReader(nil))
}

func (m *IMAPMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	from, to, subject, textBody, contentType, err := parseMessage(raw)
	if err != nil {
		m.log().Warn("imap: append rejected", "reason", "cannot parse message", "size", len(raw), "err", err)
		return errors.New("cannot parse appended message")
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	err = m.user.backend.graph.CreateMessage(ctx, m.user.username, m.folder, to, subject, textBody, contentType, from)
	if err != nil {
		m.log().Error("imap: append failed", "size", len(raw), "err", err)
		return err
	}
	m.log().Info("imap: message appended", "size", len(raw))
	return nil
}

func (m *IMAPMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, operation imap.FlagsOp, flags []string) error {
	if !containsSeen(flags) {
		return nil // only \Seen is supported
	}
	if operation != imap.AddFlags && operation != imap.RemoveFlags {
		return errors.New("unsupported flags operation")
	}

	mails, err := m.list()
	if err != nil {
		return err
	}

	seen := operation == imap.AddFlags
	var updated int
	for i, mail := range mails {
		if !seqset.Contains(uint32(i + 1)) {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		err := m.user.backend.graph.SetRead(ctx, m.user.username, mail.ID, seen)
		cancel()
		if err != nil {
			m.log().Error("imap: updating read state failed", "message_id", mail.ID, "seen", seen, "err", err)
			return err
		}
		updated++
	}
	m.log().Info("imap: read state updated", "set", seqset.String(), "messages", updated, "seen", seen)
	return nil
}

func containsSeen(flags []string) bool {
	for _, f := range flags {
		if f == imap.SeenFlag {
			return true
		}
	}
	return false
}

func (m *IMAPMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errors.New("COPY is not supported")
}

// ResolveFolder maps an IMAP mailbox name (as returned by LIST) to the Graph
// folder id / well-known name.
func (u *IMAPUser) ResolveFolder(name string) (string, error) {
	if name == "INBOX" {
		return "inbox", nil
	}
	if well, ok := graph.WellKnownFolders[strings.ToUpper(name)]; ok {
		return well, nil
	}
	folders, err := u.folders()
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if strings.EqualFold(f.DisplayName, name) {
			return wellKnownFor(f), nil
		}
	}
	return "", backend.ErrNoSuchMailbox
}

// MoveMessages implements backend.MoveMailbox using Graph message moves.
func (m *IMAPMailbox) MoveMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	destFolder, err := m.user.ResolveFolder(dest)
	if err != nil {
		return err
	}

	mails, err := m.list()
	if err != nil {
		return err
	}

	var lastErr error
	var moved int
	for i, mail := range mails {
		if !seqset.Contains(uint32(i + 1)) {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		err := m.user.backend.graph.MoveMessage(ctx, m.user.username, mail.ID, destFolder)
		cancel()
		if err != nil {
			m.log().Error("imap: moving message failed", "dest", dest, "message_id", mail.ID, "err", err)
			lastErr = err
			continue
		}
		moved++
	}
	m.log().Info("imap: messages moved", "set", seqset.String(), "dest", dest, "messages", moved)
	return lastErr
}

func (m *IMAPMailbox) Expunge() error {
	return errors.New("EXPUNGE is not supported")
}

