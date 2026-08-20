// Package notifications provides bounded, asynchronous email alerts for
// typed GameNode lifecycle events. It accepts no arbitrary message bodies or
// SMTP commands from API callers.
package notifications

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxRecipients = 20

var ErrPersistence = errors.New("email alert settings could not be persisted")

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

type Events struct {
	Started           bool `json:"started"`
	Stopped           bool `json:"stopped"`
	Crashed           bool `json:"crashed"`
	Restarted         bool `json:"restarted"`
	AutoRestartFailed bool `json:"auto_restart_failed"`
	AutoRestartLimit  bool `json:"auto_restart_limit"`
}

type Configuration struct {
	Provider              string   `json:"provider"`
	GraphTenantID         string   `json:"graph_tenant_id"`
	GraphClientID         string   `json:"graph_client_id"`
	GraphSender           string   `json:"graph_sender"`
	GraphSecretConfigured bool     `json:"graph_secret_configured"`
	Enabled               bool     `json:"enabled"`
	SMTPHost              string   `json:"smtp_host"`
	SMTPPort              int      `json:"smtp_port"`
	SMTPSecurity          string   `json:"smtp_security"`
	SMTPUsername          string   `json:"smtp_username"`
	PasswordConfigured    bool     `json:"password_configured"`
	FromAddress           string   `json:"from_address"`
	Recipients            []string `json:"recipients"`
	SubjectPrefix         string   `json:"subject_prefix"`
	Events                Events   `json:"events"`
	password              string
	graphClientSecret     string
}

type Patch struct {
	Provider               *string      `json:"provider,omitempty"`
	GraphTenantID          *string      `json:"graph_tenant_id,omitempty"`
	GraphClientID          *string      `json:"graph_client_id,omitempty"`
	GraphClientSecret      *string      `json:"graph_client_secret,omitempty"`
	ClearGraphClientSecret *bool        `json:"clear_graph_client_secret,omitempty"`
	GraphSender            *string      `json:"graph_sender,omitempty"`
	Enabled                *bool        `json:"enabled,omitempty"`
	SMTPHost               *string      `json:"smtp_host,omitempty"`
	SMTPPort               *int         `json:"smtp_port,omitempty"`
	SMTPSecurity           *string      `json:"smtp_security,omitempty"`
	SMTPUsername           *string      `json:"smtp_username,omitempty"`
	SMTPPassword           *string      `json:"smtp_password,omitempty"`
	ClearPassword          *bool        `json:"clear_password,omitempty"`
	FromAddress            *string      `json:"from_address,omitempty"`
	Recipients             *[]string    `json:"recipients,omitempty"`
	SubjectPrefix          *string      `json:"subject_prefix,omitempty"`
	Events                 *EventsPatch `json:"events,omitempty"`
}

type EventsPatch struct {
	Started           *bool `json:"started,omitempty"`
	Stopped           *bool `json:"stopped,omitempty"`
	Crashed           *bool `json:"crashed,omitempty"`
	Restarted         *bool `json:"restarted,omitempty"`
	AutoRestartFailed *bool `json:"auto_restart_failed,omitempty"`
	AutoRestartLimit  *bool `json:"auto_restart_limit,omitempty"`
}

type Event struct {
	Type       string
	ServerID   string
	ServerName string
	TenantID   string
	ExitCode   *int
	OccurredAt time.Time
}

type Service struct {
	db     *sql.DB
	log    *slog.Logger
	queue  chan Event
	stop   chan struct{}
	done   chan struct{}
	close  sync.Once
	sender func(context.Context, Configuration, string, string) error
}

func New(db *sql.DB, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Service{db: db, log: log, queue: make(chan Event, 64), stop: make(chan struct{}), done: make(chan struct{}), sender: sendConfigured}
	go s.run()
	return s
}

func (s *Service) Close() { s.close.Do(func() { close(s.stop); <-s.done }) }

func defaults() Configuration {
	return Configuration{Provider: "smtp", SMTPPort: 587, SMTPSecurity: "starttls", SubjectPrefix: "[GameNode]", Events: Events{Crashed: true, Restarted: true, AutoRestartFailed: true, AutoRestartLimit: true}}
}

func (s *Service) Get(ctx context.Context) (Configuration, error) {
	c := defaults()
	var recipients string
	err := s.db.QueryRowContext(ctx, `SELECT enabled,smtp_host,smtp_port,smtp_security,smtp_username,smtp_password,from_address,recipients_json,subject_prefix,notify_started,notify_stopped,notify_crashed,notify_restarted,notify_auto_restart_failed,notify_auto_restart_limit,provider,graph_tenant_id,graph_client_id,graph_client_secret,graph_sender FROM email_alert_settings WHERE singleton=1`).Scan(&c.Enabled, &c.SMTPHost, &c.SMTPPort, &c.SMTPSecurity, &c.SMTPUsername, &c.password, &c.FromAddress, &recipients, &c.SubjectPrefix, &c.Events.Started, &c.Events.Stopped, &c.Events.Crashed, &c.Events.Restarted, &c.Events.AutoRestartFailed, &c.Events.AutoRestartLimit, &c.Provider, &c.GraphTenantID, &c.GraphClientID, &c.graphClientSecret, &c.GraphSender)
	if err != nil {
		return Configuration{}, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	if err = json.Unmarshal([]byte(recipients), &c.Recipients); err != nil {
		return Configuration{}, fmt.Errorf("%w: invalid recipients", ErrPersistence)
	}
	c.PasswordConfigured = c.password != ""
	c.GraphSecretConfigured = c.graphClientSecret != ""
	if err = validate(c, c.Enabled); err != nil {
		return Configuration{}, fmt.Errorf("%w: invalid configuration", ErrPersistence)
	}
	return c, nil
}

func (s *Service) Update(ctx context.Context, p Patch) (Configuration, []string, error) {
	if p.SMTPPassword != nil && p.ClearPassword != nil && *p.ClearPassword {
		return Configuration{}, nil, errors.New("SMTP password and clear_password cannot be supplied together")
	}
	c, err := s.Get(ctx)
	if err != nil {
		return Configuration{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	changed := []string{}
	setString := func(dst *string, value *string, field string) {
		if value != nil && *dst != strings.TrimSpace(*value) {
			*dst = strings.TrimSpace(*value)
			changed = append(changed, field)
		}
	}
	setString(&c.Provider, p.Provider, "email_alerts.provider")
	setString(&c.GraphTenantID, p.GraphTenantID, "email_alerts.graph_tenant_id")
	setString(&c.GraphClientID, p.GraphClientID, "email_alerts.graph_client_id")
	setString(&c.GraphSender, p.GraphSender, "email_alerts.graph_sender")
	if p.GraphClientSecret != nil {
		c.graphClientSecret = *p.GraphClientSecret
		changed = append(changed, "email_alerts.graph_client_secret")
	}
	if p.ClearGraphClientSecret != nil && *p.ClearGraphClientSecret {
		c.graphClientSecret = ""
		changed = append(changed, "email_alerts.graph_client_secret")
	}
	if p.Enabled != nil && c.Enabled != *p.Enabled {
		c.Enabled = *p.Enabled
		changed = append(changed, "email_alerts.enabled")
	}
	setString(&c.SMTPHost, p.SMTPHost, "email_alerts.smtp_host")
	if p.SMTPPort != nil && c.SMTPPort != *p.SMTPPort {
		c.SMTPPort = *p.SMTPPort
		changed = append(changed, "email_alerts.smtp_port")
	}
	setString(&c.SMTPSecurity, p.SMTPSecurity, "email_alerts.smtp_security")
	setString(&c.SMTPUsername, p.SMTPUsername, "email_alerts.smtp_username")
	if p.SMTPPassword != nil {
		if len(*p.SMTPPassword) > 512 {
			return Configuration{}, nil, errors.New("SMTP password must not exceed 512 characters")
		}
		if c.password != *p.SMTPPassword {
			c.password = *p.SMTPPassword
			changed = append(changed, "email_alerts.smtp_password")
		}
	}
	if p.ClearPassword != nil && *p.ClearPassword && c.password != "" {
		c.password = ""
		changed = append(changed, "email_alerts.smtp_password")
	}
	setString(&c.FromAddress, p.FromAddress, "email_alerts.from_address")
	if p.Recipients != nil {
		normalized, e := normalizeAddresses(*p.Recipients)
		if e != nil {
			return Configuration{}, nil, e
		}
		if !equalStrings(c.Recipients, normalized) {
			c.Recipients = normalized
			changed = append(changed, "email_alerts.recipients")
		}
	}
	setString(&c.SubjectPrefix, p.SubjectPrefix, "email_alerts.subject_prefix")
	if p.Events != nil {
		applyBool := func(dst *bool, src *bool, name string) {
			if src != nil && *dst != *src {
				*dst = *src
				changed = append(changed, name)
			}
		}
		applyBool(&c.Events.Started, p.Events.Started, "email_alerts.events.started")
		applyBool(&c.Events.Stopped, p.Events.Stopped, "email_alerts.events.stopped")
		applyBool(&c.Events.Crashed, p.Events.Crashed, "email_alerts.events.crashed")
		applyBool(&c.Events.Restarted, p.Events.Restarted, "email_alerts.events.restarted")
		applyBool(&c.Events.AutoRestartFailed, p.Events.AutoRestartFailed, "email_alerts.events.auto_restart_failed")
		applyBool(&c.Events.AutoRestartLimit, p.Events.AutoRestartLimit, "email_alerts.events.auto_restart_limit")
	}
	if err = validate(c, c.Enabled); err != nil {
		return Configuration{}, nil, err
	}
	if len(changed) == 0 {
		return c, nil, nil
	}
	recipients, _ := json.Marshal(c.Recipients)
	_, err = s.db.ExecContext(ctx, `UPDATE email_alert_settings SET enabled=?,smtp_host=?,smtp_port=?,smtp_security=?,smtp_username=?,smtp_password=?,from_address=?,recipients_json=?,subject_prefix=?,notify_started=?,notify_stopped=?,notify_crashed=?,notify_restarted=?,notify_auto_restart_failed=?,notify_auto_restart_limit=?,provider=?,graph_tenant_id=?,graph_client_id=?,graph_client_secret=?,graph_sender=?,updated_at=? WHERE singleton=1`, c.Enabled, c.SMTPHost, c.SMTPPort, c.SMTPSecurity, c.SMTPUsername, c.password, c.FromAddress, string(recipients), c.SubjectPrefix, c.Events.Started, c.Events.Stopped, c.Events.Crashed, c.Events.Restarted, c.Events.AutoRestartFailed, c.Events.AutoRestartLimit, c.Provider, c.GraphTenantID, c.GraphClientID, c.graphClientSecret, c.GraphSender, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Configuration{}, nil, fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	c.PasswordConfigured = c.password != ""
	return c, changed, nil
}

func validate(c Configuration, requireComplete bool) error {
	if c.Provider != "smtp" && c.Provider != "graph" {
		return errors.New("email provider must be smtp or graph")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return errors.New("SMTP port must be between 1 and 65535")
	}
	if c.SMTPSecurity != "starttls" && c.SMTPSecurity != "tls" && c.SMTPSecurity != "none" {
		return errors.New("SMTP security must be starttls, tls, or none")
	}
	if len(c.SMTPHost) > 253 || (c.SMTPHost != "" && net.ParseIP(c.SMTPHost) == nil && !hostPattern.MatchString(c.SMTPHost)) {
		return errors.New("SMTP host is invalid")
	}
	if len(c.SMTPUsername) > 256 || strings.ContainsAny(c.SMTPUsername, "\r\n\x00") {
		return errors.New("SMTP username is invalid")
	}
	if len(c.SubjectPrefix) > 80 || strings.ContainsAny(c.SubjectPrefix, "\r\n\x00") {
		return errors.New("subject prefix is invalid")
	}
	if c.FromAddress != "" {
		if _, err := mail.ParseAddress(c.FromAddress); err != nil {
			return errors.New("from address is invalid")
		}
	}
	if _, err := normalizeAddresses(c.Recipients); err != nil {
		return err
	}
	if requireComplete && c.Provider == "smtp" && (c.SMTPHost == "" || c.FromAddress == "" || len(c.Recipients) == 0) {
		return errors.New("enabled email alerts require an SMTP host, from address, and at least one recipient")
	}
	if requireComplete && c.Provider == "graph" && (c.GraphTenantID == "" || c.GraphClientID == "" || c.graphClientSecret == "" || c.GraphSender == "" || len(c.Recipients) == 0) {
		return errors.New("enabled Microsoft Graph email alerts require tenant, client, secret, sender, and recipients")
	}
	if c.SMTPSecurity == "none" && (c.SMTPUsername != "" || c.password != "") {
		return errors.New("SMTP authentication requires TLS or STARTTLS")
	}
	return nil
}

func normalizeAddresses(values []string) ([]string, error) {
	if len(values) > MaxRecipients {
		return nil, fmt.Errorf("at most %d recipients are allowed", MaxRecipients)
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		a, err := mail.ParseAddress(raw)
		if err != nil || a.Address != raw {
			return nil, errors.New("recipient address is invalid")
		}
		key := strings.ToLower(raw)
		if !seen[key] {
			seen[key] = true
			out = append(out, raw)
		}
	}
	return out, nil
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Service) Enqueue(e Event) {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	select {
	case s.queue <- e:
	default:
		s.log.Warn("email alert queue is full; event dropped", "server_id", e.ServerID, "event", e.Type)
	}
}

func (s *Service) Test(ctx context.Context) error {
	c, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if err = validate(c, true); err != nil {
		return err
	}
	return s.sender(ctx, c, subject(c, "Test alert"), "This is a GameNode email alert test.\r\n\r\nYour SMTP configuration is working.")
}

// SendEmailVerification implements emailverification.Sender without exposing
// SMTP credentials or allowing callers to supply arbitrary message content.
func (s *Service) SendEmailVerification(ctx context.Context, recipient, token string, lifetime time.Duration) error {
	c, err := s.Get(ctx)
	if err != nil {
		return err
	}
	recipients, err := normalizeAddresses([]string{recipient})
	if err != nil {
		return err
	}
	if len(token) < 32 || len(token) > 128 || strings.ContainsAny(token, "\r\n\x00") {
		return errors.New("email verification token is invalid")
	}
	if lifetime < 5*time.Minute || lifetime > 24*time.Hour {
		return errors.New("email verification lifetime is invalid")
	}
	c.Recipients = recipients
	if err = validate(c, true); err != nil {
		return err
	}
	body := fmt.Sprintf("Verify your email address for GameNode.\r\n\r\nVerification token:\r\n%s\r\n\r\nThis token expires in %d minutes. If you did not request this, you can ignore this email.", token, int(lifetime/time.Minute))
	return s.sender(ctx, c, subject(c, "Verify your email address"), body)
}

// SendRegistrationInvitation sends the fixed, application-owned invitation
// message. The bearer token is carried only in the HTTPS link and is never
// logged or persisted in clear text by this package.
func (s *Service) SendRegistrationInvitation(ctx context.Context, recipient, tenantName, link string, lifetime time.Duration) error {
	c, err := s.Get(ctx)
	if err != nil {
		return err
	}
	recipients, err := normalizeAddresses([]string{recipient})
	if err != nil {
		return err
	}
	if len(link) < 8 || len(link) > 2048 || strings.ContainsAny(link, "\r\n\x00") {
		return errors.New("invitation link is invalid")
	}
	if lifetime < time.Minute || lifetime > 14*24*time.Hour {
		return errors.New("invitation lifetime is invalid")
	}
	c.Recipients = recipients
	if err = validate(c, true); err != nil {
		return err
	}
	body := fmt.Sprintf("You have been invited to join the GameNode tenant %s.\r\n\r\nRegister here:\r\n%s\r\n\r\nThis invitation expires in %d hours. If you did not expect this, you can ignore this email.", tenantName, link, int(lifetime/time.Hour))
	return s.sender(ctx, c, subject(c, "GameNode tenant invitation"), body)
}

func (s *Service) SendPasswordReset(ctx context.Context, recipient, link string, lifetime time.Duration) error {
	c, err := s.Get(ctx)
	if err != nil {
		return err
	}
	recipients, err := normalizeAddresses([]string{recipient})
	if err != nil {
		return err
	}
	if len(link) < 8 || len(link) > 2048 || strings.ContainsAny(link, "\r\n\x00") || lifetime < time.Minute || lifetime > 24*time.Hour {
		return errors.New("password reset link is invalid")
	}
	c.Recipients = recipients
	if err = validate(c, true); err != nil {
		return err
	}
	body := fmt.Sprintf("A password reset was requested for your GameNode account.\r\n\r\nReset your password here:\r\n%s\r\n\r\nThis link expires in %d minutes. If you did not request this, you can ignore this email.", link, int(lifetime/time.Minute))
	return s.sender(ctx, c, subject(c, "GameNode password reset"), body)
}

func (s *Service) run() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.queue:
			s.deliver(e)
		}
	}
}
func (s *Service) deliver(e Event) {
	c, err := s.Get(context.Background())
	if err != nil {
		s.log.Error("email alert settings could not be loaded", "error", err)
		return
	}
	if !c.Enabled || !enabled(c.Events, e.Type) {
		return
	}
	title := eventTitle(e.Type)
	body := fmt.Sprintf("GameNode server alert\r\n\r\nEvent: %s\r\nServer: %s\r\nServer ID: %s\r\nTenant ID: %s\r\nTime: %s\r\n", title, e.ServerName, e.ServerID, e.TenantID, e.OccurredAt.UTC().Format(time.RFC3339))
	if e.ExitCode != nil {
		body += fmt.Sprintf("Exit code: %d\r\n", *e.ExitCode)
	}
	if err = s.sender(context.Background(), c, subject(c, e.ServerName+" — "+title), body); err != nil {
		s.log.Error("email alert delivery failed", "server_id", e.ServerID, "event", e.Type, "error", err)
	} else {
		s.log.Info("email alert delivered", "server_id", e.ServerID, "event", e.Type, "recipient_count", len(c.Recipients))
	}
}
func enabled(e Events, t string) bool {
	switch t {
	case "started":
		return e.Started
	case "stopped":
		return e.Stopped
	case "crashed":
		return e.Crashed
	case "restarted":
		return e.Restarted
	case "auto_restart_failed":
		return e.AutoRestartFailed
	case "auto_restart_limit":
		return e.AutoRestartLimit
	}
	return false
}
func eventTitle(t string) string {
	switch t {
	case "started":
		return "started"
	case "stopped":
		return "stopped"
	case "crashed":
		return "crashed"
	case "restarted":
		return "restarted"
	case "auto_restart_failed":
		return "automatic restart failed"
	case "auto_restart_limit":
		return "automatic restart limit reached"
	}
	return "lifecycle event"
}
func subject(c Configuration, title string) string {
	p := strings.TrimSpace(c.SubjectPrefix)
	if p == "" {
		return title
	}
	return p + " " + title
}

func sendConfigured(ctx context.Context, c Configuration, subjectLine, body string) error {
	if c.Provider == "graph" {
		return sendGraph(ctx, c, subjectLine, body)
	}
	return sendSMTP(ctx, c, subjectLine, body)
}

func sendGraph(ctx context.Context, c Configuration, subjectLine, body string) error {
	if c.GraphTenantID == "" || c.GraphClientID == "" || c.graphClientSecret == "" || c.GraphSender == "" {
		return errors.New("Microsoft Graph configuration is incomplete")
	}
	form := url.Values{"client_id": {c.GraphClientID}, "client_secret": {c.graphClientSecret}, "scope": {"https://graph.microsoft.com/.default"}, "grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://login.microsoftonline.com/"+url.PathEscape(c.GraphTenantID)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Microsoft identity token request failed with status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&token); err != nil || token.AccessToken == "" {
		return errors.New("Microsoft identity token response invalid")
	}
	payload := map[string]any{"message": map[string]any{"subject": subjectLine, "body": map[string]string{"contentType": "Text", "content": body}, "toRecipients": recipientsPayload(c.Recipients)}, "saveToSentItems": false}
	data, _ := json.Marshal(payload)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://graph.microsoft.com/v1.0/users/"+url.PathEscape(c.GraphSender)+"/sendMail", strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Microsoft Graph sendMail failed with status %d", resp.StatusCode)
	}
	return nil
}
func recipientsPayload(values []string) []map[string]map[string]string {
	out := make([]map[string]map[string]string, 0, len(values))
	for _, v := range values {
		out = append(out, map[string]map[string]string{"emailAddress": {"address": v}})
	}
	return out
}

func sendSMTP(ctx context.Context, c Configuration, subjectLine, body string) error {
	address := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	dialer := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	tlsConfig := &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12}
	if c.SMTPSecurity == "tls" {
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	client, err := smtp.NewClient(conn, c.SMTPHost)
	if err != nil {
		return fmt.Errorf("open SMTP session: %w", err)
	}
	defer client.Close()
	if c.SMTPSecurity == "starttls" {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return errors.New("SMTP server does not offer STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if c.SMTPUsername != "" {
		if err = client.Auth(smtp.PlainAuth("", c.SMTPUsername, c.password, c.SMTPHost)); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", err)
		}
	}
	from, _ := mail.ParseAddress(c.FromAddress)
	if err = client.Mail(from.Address); err != nil {
		return fmt.Errorf("SMTP sender rejected: %w", err)
	}
	for _, raw := range c.Recipients {
		a, _ := mail.ParseAddress(raw)
		if err = client.Rcpt(a.Address); err != nil {
			return fmt.Errorf("SMTP recipient rejected: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	bw := bufio.NewWriter(w)
	_, err = fmt.Fprintf(bw, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", c.FromAddress, strings.Join(c.Recipients, ", "), mime.QEncoding.Encode("utf-8", subjectLine), time.Now().Format(time.RFC1123Z), body)
	if flushErr := bw.Flush(); err == nil {
		err = flushErr
	}
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("send SMTP message: %w", err)
	}
	return client.Quit()
}
