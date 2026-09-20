# ExchangeRelay

ExchangeRelay übersetzt **SMTP**- und **IMAP**-Anfragen in Aufrufe der **Microsoft Graph API**.
Mail-Clients und Geräte senden ihre Mails über SMTP, die der Relay via Graph (`sendMail`) versendet;
einkommende E-Mails liest der Relay über Graph aus und stellt sie per IMAP zur Verfügung.

## Features

- SMTP-Server (`EHLO`/`HELO`, `STARTTLS`, `AUTH PLAIN`, `MAIL FROM`, `RCPT TO`, `DATA`, `QUIT`)
- IMAP-Server (`CAPABILITY`, `LOGIN`, `NAMESPACE`, `LIST`, `SELECT`, `EXAMINE`,
  `STATUS`, `UID SEARCH`, `FETCH`, `STORE`, `IDLE`, `QUIT`)
- Authentifizierung und Mailversand laufen über Microsoft Graph (OAuth2 Client Credentials)
- Host- und Absender-Zulassung per `config.ini`:

  ```ini
  [host:*]
  emails = *          # alle Hosts dürfen mit allen Adressen senden

  [host:10.0.0.43]
  emails = mail@host.tld
  ```

- Ports konfigurierbar (`smtp_addr`, `imap_addr`)

## Installation

Das Skript klont das Repository nach `/opt/exchangerelay`, installiert Go (falls nötig),
baut das Binary, fragt am Ende nach TenantId, ClientId und ClientSecret, erstellt
`/etc/exchangerelay/config.ini` und startet den systemd-Service.

Am einfachsten per curl:

```bash
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/refs/heads/main/install.sh | bash
```

Da die Gebäudeanweisungen von root ausgeführt werden müssen:

```bash
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/refs/heads/main/install.sh | sudo bash
```

Alternativ herunterladen, prüfen und ausführen:

```bash
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/refs/heads/main/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

Nicht-interaktiv (z. B. für Automatisierung):

```bash
export EXCHANGERELAY_TENANT_ID="..."
export EXCHANGERELAY_CLIENT_ID="..."
export EXCHANGERELAY_CLIENT_SECRET="..."
curl -fsSL https://raw.githubusercontent.com/bbenouarets/exchangerelay/refs/heads/main/install.sh | sudo bash -s -- --yes
```

## Konfiguration

`/etc/exchangerelay/config.ini` (erzeugt vom Installer; lokal zur Entwicklung: `config.ini`):

```ini
[exchange]
tenant_id = "00000000-0000-0000-0000-000000000000"
client_id = "00000000-0000-0000-0000-000000000000"
client_secret = "hier-das-secret"

[server]
smtp_addr = "0.0.0.0:25"
imap_addr = "0.0.0.0:143"

[host:*]
emails = *
```

Erforderliche **Application-Permissions** in Microsoft Entra (Admin-Zustimmung nötig):

- `Mail.Read` (Mailboxen lesen)
- `Mail.ReadWrite` (z. B. `\Seen`-Flag setzen)
- `Mail.Send` (Versand)
- `User.Read.All` (Mailbox-Existenzprüfung / Auflistung)

## Service verwalten

```bash
systemctl status exchangerelay
journalctl -u exchangerelay -f
systemctl restart exchangerelay
```

## Lokale Entwicklung

```bash
go build .
./exchangerelay                 # liest ./config.ini
```

Für lokale Tests Ports und erlaubte Hosts in `config.ini` anpassen, z. B.:

```ini
[server]
smtp_addr = "127.0.0.1:1025"
imap_addr = "127.0.0.1:1143"

[host:127.0.0.1]
emails = support@example.com
```
