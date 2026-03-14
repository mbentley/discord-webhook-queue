package delivery

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mbentley/discord-webhook-queue/internal/alert"
	"github.com/mbentley/discord-webhook-queue/internal/config"
	"github.com/mbentley/discord-webhook-queue/internal/metrics"
	"github.com/mbentley/discord-webhook-queue/internal/store"
)

type deliveryState int

const (
	stateHealthy deliveryState = iota
	stateProbing
)

type resultKind int

const (
	kindSuccess resultKind = iota
	kindRateLimited
	kindError
)

type sendResult struct {
	kind        resultKind
	retryAfter  time.Duration // how long to wait before retrying (rate limit or error)
	ratePause   time.Duration // proactive pause when rate limit window is exhausted
	err         error
}

// Status is the current state of the delivery engine, returned to the status endpoint.
type Status struct {
	State         string     `json:"state"`
	LastFailureAt *time.Time `json:"last_failure_at"`
}

// Engine is the delivery state machine. It pulls messages from the store and
// forwards them to Discord, handling rate limits and outage detection.
type Engine struct {
	store   *store.Store
	cfg     *config.Config
	metrics *metrics.Metrics
	alerter *alert.Alerter
	client  *http.Client

	mu            sync.RWMutex
	currentState  deliveryState
	failureStart  time.Time
	lastFailureAt time.Time
}

// New creates a new delivery Engine.
func New(s *store.Store, cfg *config.Config, m *metrics.Metrics, a *alert.Alerter) *Engine {
	return &Engine{
		store:        s,
		cfg:          cfg,
		metrics:      m,
		alerter:      a,
		currentState: stateHealthy,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Status returns the current delivery state for the status endpoint.
func (e *Engine) Status() Status {
	e.mu.RLock()
	defer e.mu.RUnlock()

	s := Status{State: "healthy"}
	if e.currentState == stateProbing {
		s.State = "probing"
	}
	if !e.lastFailureAt.IsZero() {
		t := e.lastFailureAt
		s.LastFailureAt = &t
	}
	return s
}

// Run is the main delivery loop. It blocks until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	slog.Info("delivery engine started")
	for {
		select {
		case <-ctx.Done():
			slog.Info("delivery engine stopped")
			return
		default:
		}

		msg, err := e.store.NextPending()
		if err != nil {
			slog.Error("failed to fetch next pending message", "err", err)
			sleepCtx(ctx, 5*time.Second)
			continue
		}

		if msg == nil {
			sleepCtx(ctx, time.Second)
			continue
		}

		if err := e.store.MarkInFlight(msg.ID); err != nil {
			slog.Error("failed to mark message in_flight", "id", msg.ID, "err", err)
			sleepCtx(ctx, time.Second)
			continue
		}

		result := e.sendToDiscord(ctx, msg)

		switch result.kind {
		case kindSuccess:
			if err := e.store.MarkSent(msg.ID); err != nil {
				slog.Error("failed to mark message sent", "id", msg.ID, "err", err)
			}
			e.metrics.MessagesSent.Inc()
			slog.Info("message delivered",
				"id", msg.ID,
				"webhook_id", msg.WebhookID,
				"retry_count", msg.RetryCount,
			)
			e.transitionToHealthy()
			// Proactively pause if Discord told us the rate limit window is exhausted.
			if result.ratePause > 0 {
				slog.Debug("rate limit window exhausted, pausing before next send",
					"pause", result.ratePause.Round(time.Millisecond),
				)
				sleepCtx(ctx, result.ratePause)
			}
			// No additional sleep — loop immediately for the next message.

		case kindRateLimited:
			if err := e.store.MarkFailed(msg.ID, "rate limited by Discord"); err != nil {
				slog.Error("failed to mark message failed", "id", msg.ID, "err", err)
			}
			e.metrics.Retries.Inc()
			slog.Warn("rate limited by Discord",
				"id", msg.ID,
				"retry_after", result.retryAfter.Round(time.Millisecond),
			)
			sleepCtx(ctx, result.retryAfter)
			// Don't change health state — rate limits are normal Discord behaviour.

		case kindError:
			if err := e.store.MarkFailed(msg.ID, result.err.Error()); err != nil {
				slog.Error("failed to mark message failed", "id", msg.ID, "err", err)
			}
			e.metrics.MessagesFailed.Inc()
			if msg.RetryCount > 0 {
				e.metrics.Retries.Inc()
			}
			slog.Error("failed to deliver message",
				"id", msg.ID,
				"webhook_id", msg.WebhookID,
				"err", result.err,
				"attempt", msg.RetryCount+1,
			)
			e.transitionToProbing()
			depth, _ := e.store.QueueDepth()
			e.alerter.Check(depth)
			sleepCtx(ctx, e.cfg.RetryInterval())
		}
	}
}

func (e *Engine) transitionToHealthy() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.currentState != stateHealthy {
		slog.Info("delivery state: probing -> healthy")
		e.currentState = stateHealthy
		e.metrics.Healthy.Set(1)
		e.alerter.NotifyHealthy()
	}
}

func (e *Engine) transitionToProbing() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.lastFailureAt = now
	if e.currentState != stateProbing {
		slog.Warn("delivery state: healthy -> probing")
		e.currentState = stateProbing
		e.failureStart = now
		e.metrics.Healthy.Set(0)
		e.alerter.NotifyUnhealthy(now)
	}
}

func (e *Engine) sendToDiscord(ctx context.Context, msg *store.Message) sendResult {
	url := fmt.Sprintf("https://discord.com/api/webhooks/%s/%s", msg.WebhookID, msg.WebhookToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(msg.Payload))
	if err != nil {
		return sendResult{kind: kindError, err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", msg.ContentType)
	req.Header.Set("User-Agent", "discord-webhook-queue/1.0 (github.com/mbentley/discord-webhook-queue)")

	resp, err := e.client.Do(req)
	if err != nil {
		// Context cancellation during send is not a Discord failure.
		if ctx.Err() != nil {
			return sendResult{kind: kindError, err: fmt.Errorf("send cancelled: %w", ctx.Err())}
		}
		return sendResult{kind: kindError, err: fmt.Errorf("http send: %w", err)}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		return sendResult{
			kind:       kindRateLimited,
			retryAfter: parseRetryAfter(resp),
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return sendResult{
			kind:      kindSuccess,
			ratePause: parseRatePause(resp),
		}
	}

	return sendResult{
		kind: kindError,
		err:  fmt.Errorf("discord returned HTTP %d", resp.StatusCode),
	}
}

// parseRetryAfter reads the Retry-After header from a 429 response.
func parseRetryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 5 * time.Second // safe fallback
}

// parseRatePause returns a suggested pause duration when X-RateLimit-Remaining
// hits 0 on a successful response, preventing a guaranteed immediate 429.
func parseRatePause(resp *http.Response) time.Duration {
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return 0
	}
	if v := resp.Header.Get("X-RateLimit-Reset-After"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	}
	return 0
}

// sleepCtx sleeps for d, but returns early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
