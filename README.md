# ExchangeRelay

ExchangeRelay translates **SMTP** and **IMAP** requests into calls to the **Microsoft Graph API**.
Mail clients and devices send their e-mails over SMTP, and the relay dispatches them through
Graph (`sendMail`); incoming e-mails are read through Graph and exposed via IMAP.

## Features

- SMTP server (`EHLO`/`HELO`, `STARTTLS`, `AUTH PLAIN`, `MAIL FROM`, `RCPT TO`, `DATA`, `QUIT`)
- IMAP server (`CAPABILITY`, `LOGIN`, `NAMESPACE`, `LIST`, `SELECT`, `EXAMINE`,
  `STATUS`, `UID SEARCH`, `FETCH`, `STORE`, `IDLE`, `QUIT`)
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
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/main/install.sh | sudo bash
```

Or download first, review, and run:

```bash
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/main/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Non-interactive (e.g. for automation):

```bash
export EXCHANGERELAY_TENANT_ID="..."
export EXCHANGERELAY_CLIENT_ID="..."
export EXCHANGERELAY_CLIENT_SECRET="..."
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/main/install.sh -o install.sh
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
