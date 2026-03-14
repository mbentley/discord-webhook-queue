package alert

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"sync"
	"time"

	"github.com/mbentley/discord-webhook-queue/internal/config"
)

// Alerter tracks Discord failure state and sends SMTP alerts when thresholds are crossed.
type Alerter struct {
	cfg *config.Config

	mu            sync.Mutex
	failing       bool
	failureStart  time.Time
	lastAlertSent time.Time
}

// New creates a new Alerter.
func New(cfg *config.Config) *Alerter {
	return &Alerter{cfg: cfg}
}

// NotifyUnhealthy is called by the delivery engine when it enters probing mode.
// failureStart is the time the first failure occurred.
func (a *Alerter) NotifyUnhealthy(failureStart time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.failing {
		a.failing = true
		a.failureStart = failureStart
		a.lastAlertSent = time.Time{} // reset so this new outage triggers its own alert
	}
}

// NotifyHealthy is called by the delivery engine when delivery recovers.
// Resets outage state so the next failure is treated as a new incident.
func (a *Alerter) NotifyHealthy() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failing = false
	a.failureStart = time.Time{}
	a.lastAlertSent = time.Time{}
}

// Check evaluates whether an alert email should be sent and sends it if so.
// Should be called by the delivery engine after each failed delivery attempt.
func (a *Alerter) Check(queueDepth int) {
	if !a.cfg.SMTPEnabled() {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.failing {
		return
	}

	now := time.Now()
	failureDuration := now.Sub(a.failureStart)

	if failureDuration < a.cfg.FailureAlertThreshold() {
		return
	}

	// Send if: no alert yet for this outage, or 24h have passed since the last one.
	if !a.lastAlertSent.IsZero() && now.Sub(a.lastAlertSent) < a.cfg.RepeatAlertInterval() {
		return
	}

	if err := a.sendEmail(failureDuration, queueDepth); err != nil {
		slog.Error("failed to send SMTP alert", "err", err)
		return
	}

	a.lastAlertSent = now
	slog.Info("SMTP alert sent", "failure_duration", failureDuration.Round(time.Second))
}

func (a *Alerter) sendEmail(failureDuration time.Duration, queueDepth int) error {
	_, port, err := net.SplitHostPort(a.cfg.ListenAddr)
	if err != nil || port == "" {
		port = "8080"
	}
	statusURL := fmt.Sprintf("http://%s:%s/status", a.cfg.AlertHostLabel, port)

	subject := fmt.Sprintf("[discord-webhook-queue] %s: Discord delivery failing for %s",
		a.cfg.AlertHostLabel,
		failureDuration.Round(time.Second),
	)

	msg := a.buildMessage(subject,
		fmt.Sprintf(
			"discord-webhook-queue on %s has been unable to deliver messages to Discord.\r\n"+
				"\r\n"+
				"Failure duration : %s\r\n"+
				"Pending messages : %d\r\n"+
				"\r\n"+
				"Check queue status at: %s\r\n",
			a.cfg.AlertHostLabel,
			failureDuration.Round(time.Second),
			queueDepth,
			statusURL,
		),
	)

	return a.dispatch(msg)
}

// SendTest sends a test alert email to verify SMTP configuration. Returns an
// error if SMTP is not configured or if the send fails.
func (a *Alerter) SendTest() error {
	if !a.cfg.SMTPEnabled() {
		return fmt.Errorf("SMTP not configured")
	}

	subject := fmt.Sprintf("[discord-webhook-queue] %s: TEST alert email", a.cfg.AlertHostLabel)

	msg := a.buildMessage(subject,
		fmt.Sprintf(
			"This is a TEST alert from discord-webhook-queue on %s.\r\n"+
				"\r\n"+
				"If you received this, your SMTP configuration is working correctly.\r\n"+
				"\r\n"+
				"Failure duration : N/A (test)\r\n"+
				"Pending messages : 0\r\n",
			a.cfg.AlertHostLabel,
		),
	)

	return a.dispatch(msg)
}

func (a *Alerter) buildMessage(subject, bodyText string) []byte {
	return []byte(fmt.Sprintf(
		"Subject: %s\r\n"+
			"From: %s\r\n"+
			"To: %s\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n"+
			"\r\n"+
			"%s",
		subject,
		a.cfg.SMTPFrom,
		a.cfg.SMTPTo,
		bodyText,
	))
}

func (a *Alerter) dispatch(msg []byte) error {
	addr := fmt.Sprintf("%s:%d", a.cfg.SMTPHost, a.cfg.SMTPPort)

	var auth smtp.Auth
	if a.cfg.SMTPUsername != "" {
		auth = smtp.PlainAuth("", a.cfg.SMTPUsername, a.cfg.SMTPPassword, a.cfg.SMTPHost)
	}

	if a.cfg.SMTPStartTLS {
		return sendWithStartTLS(addr, a.cfg.SMTPHost, auth, a.cfg.SMTPFrom, a.cfg.SMTPTo, msg)
	}

	return smtp.SendMail(addr, auth, a.cfg.SMTPFrom, []string{a.cfg.SMTPTo}, msg)
}

func sendWithStartTLS(addr, host string, auth smtp.Auth, from, to string, body []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}

	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}

	return c.Quit()
}
