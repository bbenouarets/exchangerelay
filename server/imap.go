package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
	if password == "" {
		return nil, backend.ErrInvalidCredentials
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	exists, err := b.graph.MailboxExists(ctx, username)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, backend.ErrInvalidCredentials
	}

	return &IMAPUser{backend: b, username: username}, nil
}

type IMAPUser struct {
	backend  *IMAPBackend
	username string
}

func (u *IMAPUser) Username() string { return u.username }

func (u *IMAPUser) folders() ([]graph.Folder, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	return u.backend.graph.ListFolders(ctx, u.username)
}

func (u *IMAPUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	folders, err := u.folders()
	if err != nil {
		return nil, err
	}

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

func (m *IMAPMailbox) list() ([]graph.Mail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	return m.user.backend.graph.ListFolderMessages(ctx, m.user.username, m.folder, imapMaxMessages)
}

func (m *IMAPMailbox) raw(mailID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	return m.user.backend.graph.GetMessageRaw(ctx, m.user.username, mailID)
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

	messages, err := m.collect(uid, seqset)
	if err != nil {
		return err
	}

	for i := range messages {
		seqNum := messages[i].uid
		msg, err := messages[i].fetch(seqNum, items)
		if err != nil {
			continue
		}
		ch <- msg
	}
	return nil
}

func (m *IMAPMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	mails, err := m.list()
	if err != nil {
		return nil, err
	}

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
	return errors.New("APPEND is not supported")
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

	for i, mail := range mails {
		if !seqset.Contains(uint32(i + 1)) {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		err := m.user.backend.graph.SetRead(ctx, m.user.username, mail.ID, operation == imap.AddFlags)
		cancel()
		if err != nil {
			return err
		}
	}
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

func (m *IMAPMailbox) Expunge() error {
	return errors.New("EXPUNGE is not supported")
}

