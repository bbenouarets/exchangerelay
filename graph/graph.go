package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	UserID       string

	httpClient *http.Client
	token      string
	tokenExp   time.Time
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

type Mail struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	BodyPrev  string    `json:"bodyPreview"`
	FromName  string    `json:"fromName"`
	FromAddr  string    `json:"fromAddress"`
	ToAddrs   []string  `json:"toAddresses"`
	Received  time.Time `json:"receivedDateTime"`
	IsRead    bool      `json:"isRead"`
	HasAttach bool      `json:"hasAttachments"`
}

type listResponse struct {
	Value []struct {
		ID           string `json:"id"`
		Subject      string `json:"subject"`
		BodyPreview  string `json:"bodyPreview"`
		ReceivedTime string `json:"receivedDateTime"`
		IsRead       bool   `json:"isRead"`
		HasAtt       bool   `json:"hasAttachments"`
		ToRecipients []struct {
			EmailAddress struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"emailAddress"`
		} `json:"toRecipients"`
		From *struct {
			EmailAddress struct {
				Name    string `json:"name"`
				Address string `json:"address"`
			} `json:"emailAddress"`
		} `json:"from"`
	} `json:"value"`
}

type Mailbox struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
}

type sendMailRequest struct {
	Message      messagePayload `json:"message"`
	SaveToSentItems bool         `json:"saveToSentItems"`
}

type messagePayload struct {
	Subject      string      `json:"subject"`
	Body         bodyItem    `json:"body"`
	From         *address    `json:"from"`
	ToRecipients []recipient `json:"toRecipients"`
	CcRecipients []recipient `json:"ccRecipients,omitempty"`
}

type bodyItem struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type address struct {
	EmailAddress emailAddress `json:"emailAddress"`
}

type emailAddress struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

type recipient struct {
	EmailAddress emailAddress `json:"emailAddress"`
}

type mailboxResponse struct {
	NextLink string `json:"@odata.nextLink"`
	Value []struct {
		ID                string `json:"id"`
		DisplayName       string `json:"displayName"`
		UserPrincipalName string `json:"userPrincipalName"`
		Mail              string `json:"mail"`
	} `json:"value"`
}

func NewClient(tenantID, clientID, clientSecret, userID string) *Client {
	return &Client{
		TenantID:     tenantID,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		UserID:       userID,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// SendMail sends a message from the given mailbox via POST /users/{mailbox}/sendMail.
func (c *Client) SendMail(ctx context.Context, from string, to, cc []string, subject, body, contentType string) error {
	token, err := c.getToken(ctx)
	if err != nil {
		return err
	}

	recipients := func(addrs []string) []recipient {
		out := make([]recipient, 0, len(addrs))
		for _, a := range addrs {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			out = append(out, recipient{EmailAddress: emailAddress{Address: a}})
		}
		return out
	}

	payload := sendMailRequest{
		SaveToSentItems: true,
		Message: messagePayload{
			Subject: subject,
			Body:    bodyItem{ContentType: contentType, Content: body},
			From:    &address{EmailAddress: emailAddress{Address: from}},
			ToRecipients: recipients(to),
		},
	}
	if len(cc) > 0 {
		payload.Message.CcRecipients = recipients(cc)
	}

	j, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/sendMail", url.PathEscape(from))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(j)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		var errBody struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("graph: sendMail failed with status %d: %s %s", resp.StatusCode, errBody.Error.Code, errBody.Error.Message)
	}
	return nil
}

func (c *Client) getToken(ctx context.Context) (string, error) {
	if c.token != "" && time.Now().Before(c.tokenExp.Add(-2*time.Minute)) {
		return c.token, nil
	}

	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", c.TenantID)
	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("graph: token request failed with status %d (check tenant_id/client_id/client_secret)", resp.StatusCode)
	}

	c.token = tr.AccessToken
	c.tokenExp = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return c.token, nil
}

// ListMailboxes returns all users with a mailbox in the tenant (follows pagination).
func (c *Client) ListMailboxes(ctx context.Context) ([]Mailbox, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	var mailboxes []Mailbox
	endpoint := "https://graph.microsoft.com/v1.0/users?$select=id,displayName,userPrincipalName,mail"

	for endpoint != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			return nil, fmt.Errorf("graph: list mailboxes failed with status %d: %s %s", resp.StatusCode, body.Error.Code, body.Error.Message)
		}

		var lr mailboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, u := range lr.Value {
			mailboxes = append(mailboxes, Mailbox{
				ID:                u.ID,
				DisplayName:       u.DisplayName,
				UserPrincipalName: u.UserPrincipalName,
				Mail:              u.Mail,
			})
		}
		endpoint = lr.NextLink
	}

	return mailboxes, nil
}

func (c *Client) ListMails(ctx context.Context, top int) ([]Mail, error) {
	return c.ListMailsFor(ctx, c.UserID, top)
}

// ListMailsFor lists the newest messages of the given mailbox (ordered by receivedDateTime desc).
func (c *Client) ListMailsFor(ctx context.Context, mailbox string, top int) ([]Mail, error) {
	if top <= 0 {
		top = 10
	}

	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages", url.PathEscape(mailbox))
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("$top", fmt.Sprintf("%d", top))
	q.Set("$select", "id,subject,bodyPreview,receivedDateTime,isRead,hasAttachments,from,toRecipients")
	q.Set("$orderby", "receivedDateTime desc")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return nil, fmt.Errorf("graph: list mails failed with status %d: %s %s", resp.StatusCode, body.Error.Code, body.Error.Message)
	}

	var lr listResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}

	mails := make([]Mail, 0, len(lr.Value))
	for _, item := range lr.Value {
		m := Mail{
			ID:        item.ID,
			Subject:   item.Subject,
			BodyPrev:  item.BodyPreview,
			Received:  parseTime(item.ReceivedTime),
			IsRead:    item.IsRead,
			HasAttach: item.HasAtt,
		}
		if item.From != nil {
			m.FromName = item.From.EmailAddress.Name
			m.FromAddr = item.From.EmailAddress.Address
		}
		for _, r := range item.ToRecipients {
			m.ToAddrs = append(m.ToAddrs, r.EmailAddress.Address)
		}
		mails = append(mails, m)
	}
	return mails, nil
}

// GetMessageRaw returns the full MIME message body (RFC 822) of a message.
func (c *Client) GetMessageRaw(ctx context.Context, mailbox, messageID string) ([]byte, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages/%s/$value", url.PathEscape(mailbox), url.PathEscape(messageID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graph: get message failed with status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// MarkRead marks a message as read.
func (c *Client) MarkRead(ctx context.Context, mailbox, messageID string) error {
	token, err := c.getToken(ctx)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages/%s", url.PathEscape(mailbox), url.PathEscape(messageID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(`{"isRead": true}`))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graph: mark read failed with status %d", resp.StatusCode)
	}
	return nil
}

// MailboxExists reports whether the given mailbox exists in the tenant.
func (c *Client) MailboxExists(ctx context.Context, mailbox string) (bool, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return false, err
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s", url.PathEscape(mailbox))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("graph: check mailbox failed with status %d", resp.StatusCode)
	}
}

// SetRead sets the read state of a message.
func (c *Client) SetRead(ctx context.Context, mailbox, messageID string, read bool) error {
	token, err := c.getToken(ctx)
	if err != nil {
		return err
	}

	body := `{"isRead": false}`
	if read {
		body = `{"isRead": true}`
	}

	endpoint := fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s/messages/%s", url.PathEscape(mailbox), url.PathEscape(messageID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("graph: set read failed with status %d", resp.StatusCode)
	}
	return nil
}
