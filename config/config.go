package config

import (
	"net"
	"strings"

	"gopkg.in/ini.v1"
)

type ConfigProvider struct {
	Path string `json:"path"`
}

type Config struct {
	Exchange       Exchange     `json:"exchange"`
	Server         ServerConfig `json:"server"`
	AllowedSenders []Sender     `json:"allowedSenders"`
}

type ServerConfig struct {
	SmtpAddr    string `json:"smtpAddr" ini:"smtp_addr"`
	ImapAddr    string `json:"imapAddr" ini:"imap_addr"`
	SmtpTlsAddr string `json:"smtpTlsAddr" ini:"smtp_tls_addr"`
	ImapTlsAddr string `json:"imapTlsAddr" ini:"imap_tls_addr"`
}

type Exchange struct {
	TenantID     string `json:"tenantId" ini:"tenant_id"`
	ClientID     string `json:"clientId" ini:"client_id"`
	ClientSecret string `json:"clientSecret" ini:"client_secret"`
}

type Sender struct {
	IPAddress string   `json:"ipAddress"`
	Emails    []string `json:"emails"`
}

func NewConfigProvider(path string) *ConfigProvider {
	return &ConfigProvider{Path: path}
}

func (c *ConfigProvider) Load() (*Config, error) {
	cfg, err := ini.Load(c.Path)
	if err != nil {
		return nil, err
	}

	config := &Config{}
	if err := cfg.Section("exchange").MapTo(&config.Exchange); err != nil {
		return nil, err
	}
	if err := cfg.Section("server").MapTo(&config.Server); err != nil {
		return nil, err
	}

	if config.Server.SmtpAddr == "" {
		config.Server.SmtpAddr = "127.0.0.1:1025"
	}
	if config.Server.ImapAddr == "" {
		config.Server.ImapAddr = "127.0.0.1:1143"
	}

	for _, section := range cfg.Sections() {
		name := section.Name()
		if name == ini.DefaultSection || name == "exchange" || name == "server" {
			continue
		}

		// Host sections use the "host:" prefix, e.g. "[host:10.0.0.43]"
		// or "[host:*]" to allow every host.
		if !strings.HasPrefix(name, "host:") {
			continue
		}
		host := strings.TrimPrefix(name, "host:")
		if host != "*" && net.ParseIP(host) == nil {
			continue
		}

		sender := Sender{
			IPAddress: host,
			Emails:    section.Key("emails").Strings(","),
		}
		config.AllowedSenders = append(config.AllowedSenders, sender)
	}

	return config, nil
}
