package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	minRetryIntervalSeconds = 5
	maxRetryIntervalSeconds = 300
	defaultRetrySeconds     = 30

	defaultFailureAlertMinutes = 15
	minFailureAlertMinutes     = 1
	maxFailureAlertMinutes     = 1440 // 24 hours
	repeatAlertHours           = 24
)

type Config struct {
	ListenAddr              string
	DBPath                  string
	RetryIntervalSeconds    int
	FailureAlertAfterMinutes int

	AuthToken  string
	AuthHeader string

	SMTPHost     string
	SMTPPort     int
	SMTPFrom     string
	SMTPTo       string
	SMTPStartTLS bool
	SMTPUsername string
	SMTPPassword string

	AlertHostLabel string
}

func Load() (*Config, error) {
	c := &Config{
		ListenAddr:               getEnv("LISTEN_ADDR", ":8080"),
		DBPath:                   getEnv("DB_PATH", "/data/queue.db"),
		RetryIntervalSeconds:     defaultRetrySeconds,
		FailureAlertAfterMinutes: defaultFailureAlertMinutes,
		AuthToken:            os.Getenv("AUTH_TOKEN"),
		AuthHeader:           getEnv("AUTH_HEADER", "X-Auth-Token"),
		SMTPHost:             os.Getenv("SMTP_HOST"),
		SMTPPort:             25,
		SMTPFrom:             os.Getenv("SMTP_FROM"),
		SMTPTo:               os.Getenv("SMTP_TO"),
		SMTPStartTLS:         false,
		SMTPUsername:         os.Getenv("SMTP_USERNAME"),
		SMTPPassword:         os.Getenv("SMTP_PASSWORD"),
		AlertHostLabel:       getEnv("ALERT_HOST_LABEL", hostname()),
	}

	if v := os.Getenv("FAILURE_ALERT_AFTER_MINUTES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("FAILURE_ALERT_AFTER_MINUTES must be an integer: %w", err)
		}
		if n < minFailureAlertMinutes || n > maxFailureAlertMinutes {
			return nil, fmt.Errorf("FAILURE_ALERT_AFTER_MINUTES must be between %d and %d", minFailureAlertMinutes, maxFailureAlertMinutes)
		}
		c.FailureAlertAfterMinutes = n
	}

	if v := os.Getenv("RETRY_INTERVAL_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("RETRY_INTERVAL_SECONDS must be an integer: %w", err)
		}
		if n < minRetryIntervalSeconds || n > maxRetryIntervalSeconds {
			return nil, fmt.Errorf("RETRY_INTERVAL_SECONDS must be between %d and %d", minRetryIntervalSeconds, maxRetryIntervalSeconds)
		}
		c.RetryIntervalSeconds = n
	}

	if v := os.Getenv("SMTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("SMTP_PORT must be an integer: %w", err)
		}
		c.SMTPPort = n
	}

	if os.Getenv("SMTP_STARTTLS") == "true" {
		c.SMTPStartTLS = true
	}

	return c, nil
}

func (c *Config) RetryInterval() time.Duration {
	return time.Duration(c.RetryIntervalSeconds) * time.Second
}

func (c *Config) SMTPEnabled() bool {
	return c.SMTPHost != "" && c.SMTPFrom != "" && c.SMTPTo != ""
}

func (c *Config) FailureAlertThreshold() time.Duration {
	return time.Duration(c.FailureAlertAfterMinutes) * time.Minute
}

func (c *Config) RepeatAlertInterval() time.Duration {
	return repeatAlertHours * time.Hour
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
