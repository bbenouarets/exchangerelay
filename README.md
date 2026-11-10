# ExchangeRelay

ExchangeRelay translates **SMTP** and **IMAP** requests into calls to the **Microsoft Graph API**.
Mail clients and devices send their e-mails over SMTP, and the relay dispatches them through
Graph (`sendMail`); incoming e-mails are read through Graph and exposed via IMAP.

## Features

- SMTP server (`EHLO`/`HELO`, `STARTTLS`, `AUTH PLAIN`, `MAIL FROM`, `RCPT TO`, `DATA`, `QUIT`)
- IMAP server (`CAPABILITY`, `LOGIN`, `NAMESPACE`, `LIST`, `SELECT`, `EXAMINE`,
  `STATUS`, `UID SEARCH`, `FETCH`, `STORE`, `IDLE`, `QUIT`)
- IMAP exposes **real Exchange folders** (Inbox, Sent Items, Drafts, Deleted Items, ...)
  via `LIST`/`SELECT` backed by Graph `mailFolders`
- Optional implicit-TLS listeners (`imap_tls_addr`, `smtp_tls_addr`) for IMAPS (993) and SMTPS (465)
- Authentication and mail delivery run through Microsoft Graph (OAuth2 client credentials)
- Permitted hosts and senders are configured per host in `config.ini`:

  ```ini
  [host:*]
  emails = *          # all hosts may send from any address

  [host:10.0.0.43]
  emails = mail@host.tld
  ```

- Configurable ports (`smtp_addr`, `imap_addr`)

## Installation

The installer clones the repository to `/opt/exchangerelay`, installs Go (if missing),
builds the binary, asks at the end for the TenantId, ClientId and ClientSecret, writes
`/etc/exchangerelay/config.ini` and starts a systemd service.

Easiest way via curl:

```bash
curl -fsSL -H "Accept: application/vnd.github.raw" https://api.github.com/repos/bbenouarets/exchangerelay/contents/install.sh?ref=main | sudo bash
```

Or download first, review, and run:

```bash
curl -fsSL -H "Accept: application/vnd.github.raw" https://api.github.com/repos/bbenouarets/exchangerelay/contents/install.sh?ref=main -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Non-interactive (e.g. for automation):

```bash
export EXCHANGERELAY_TENANT_ID="..."
export EXCHANGERELAY_CLIENT_ID="..."
export EXCHANGERELAY_CLIENT_SECRET="..."
curl -fsSL -H "Accept: application/vnd.github.raw" https://api.github.com/repos/bbenouarets/exchangerelay/contents/install.sh?ref=main -o install.sh
sudo bash install.sh --yes
```

If an existing installation is found, the script asks whether the old files
(`/opt/exchangerelay`, the binary, and `/etc/exchangerelay`) should be removed first.

## Configuration

`/etc/exchangerelay/config.ini` (created by the installer; locally for development: `config.ini`):

```ini
[exchange]
tenant_id = "00000000-0000-0000-0000-000000000000"
client_id = "00000000-0000-0000-0000-000000000000"
client_secret = "your-secret-here"

[server]
smtp_addr = "0.0.0.0:25"
imap_addr = "0.0.0.0:143"

# optional implicit-TLS listeners for clients that expect IMAPS/SMTPS
imap_tls_addr = "0.0.0.0:993"
smtp_tls_addr = "0.0.0.0:465"

[log]
level = info        # debug | info (default) | warn | error

[host:*]
emails = *
```

Required **application permissions** in Microsoft Entra (admin consent needed):

- `Mail.Read` (read mailboxes)
- `Mail.ReadWrite` (e.g. set the `\Seen` flag)
- `Mail.Send` (send mail)
- `User.Read.All` (mailbox existence check / listing)

## Managing the service

```bash
systemctl status exchangerelay
journalctl -u exchangerelay -f
systemctl restart exchangerelay
```

### Logging

The relay logs to stderr, which systemd forwards to the journal. Every line carries its
syslog priority, so filtering by severity works:

```bash
journalctl -u exchangerelay -f              # follow everything
journalctl -u exchangerelay -p warning      # only warnings and errors
```

At the default level `info` the journal shows

- every client connection (`connection opened` / `connection closed` with remote address,
  duration and transferred bytes),
- IMAP logins/logouts and every mailbox access (folder list, mailbox status, fetched,
  searched, appended, moved messages, read-state changes) with user, folder and counts,
- SMTP authentication, rejected senders and every relayed message (envelope sender,
  sending mailbox, recipients, size),
- failures talking to Microsoft Graph.

```
INFO  connection opened proto=imap remote=10.0.0.43:56491
INFO  imap: login remote=10.0.0.43:56491 user=support@example.com tls=false
INFO  imap: mailbox status remote=10.0.0.43:56491 user=support@example.com folder=INBOX messages=10 unseen=2
INFO  imap: messages fetched remote=10.0.0.43:56491 user=support@example.com folder=INBOX set=1:3 messages=3 bytes=48211 duration=412ms
INFO  connection closed proto=imap remote=10.0.0.43:56491 duration=1.2s rx_bytes=127 tx_bytes=51034
```

Set `level = debug` in the `[log]` section to additionally log every single Graph request
(method, URL without query, status, duration) and each SMTP protocol step; `warn` reduces
the output to problems only. Passwords, access tokens and message contents are never logged.

Updating an existing installation (pull + rebuild + restart):

```bash
sudo bash /opt/exchangerelay/update.sh
```

## Local development

```bash
go build .
./exchangerelay                 # reads ./config.ini
```

Adjust ports and allowed hosts in `config.ini` for local testing, e.g.:

```ini
[server]
smtp_addr = "127.0.0.1:1025"
imap_addr = "127.0.0.1:1143"

[host:127.0.0.1]
emails = support@example.com
```
