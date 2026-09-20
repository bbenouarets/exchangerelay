package server

import (
	imap "github.com/emersion/go-imap"
	imapserver "github.com/emersion/go-imap/server"
)

// NamespaceExtension implements the NAMESPACE extension (RFC 2342).
// The relay exposes a single personal namespace with INBOX only.
type NamespaceExtension struct{}

func (NamespaceExtension) Capabilities(conn imapserver.Conn) []string {
	return []string{"NAMESPACE"}
}

func (NamespaceExtension) Command(name string) imapserver.HandlerFactory {
	if name != "NAMESPACE" {
		return nil
	}
	return func() imapserver.Handler { return &NamespaceHandler{} }
}

type NamespaceHandler struct{}

func (h *NamespaceHandler) Parse(fields []interface{}) error {
	return nil
}

func (h *NamespaceHandler) Handle(conn imapserver.Conn) error {
	return conn.WriteResp(&NamespaceData{})
}

// NamespaceData writes the personal namespace for the user.
type NamespaceData struct{}

func (d *NamespaceData) WriteTo(w *imap.Writer) error {
	// "* NAMESPACE (("" "/")) NIL NIL"
	_, err := w.Write([]byte("* NAMESPACE ((\"\" \"/\")) NIL NIL\r\n"))
	return err
}
