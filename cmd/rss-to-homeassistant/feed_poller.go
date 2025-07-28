package main

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/function61/gokit/log/logex"
	"github.com/function61/hautomo/pkg/homeassistant"
)

const (
	// Circuit breaker thresholds
	maxFailures       = 3
	resetTimeout      = 5 * time.Minute
	initialBackoff    = 1 * time.Second
	maxBackoff        = 5 * time.Minute
	backoffMultiplier = 2.0
)

// feedPoller manages the polling of a single RSS feed with resilience features
type feedPoller struct {
	feedConfig configRSSFeed
	ha         *homeassistant.MqttClient
	log       *logex.Leveled
	pollFunc  func(context.Context) error
	
	// Circuit breaker state
	failureCount int32
	lastFailure  time.Time
}

// newFeedPoller creates a new feedPoller instance
func newFeedPoller(
	feedConfig configRSSFeed,
	ha *homeassistant.MqttClient,
	log *logex.Leveled,
	pollFunc func(context.Context) error,
) *feedPoller {
	return &feedPoller{
		feedConfig: feedConfig,
		ha:         ha,
		log:       log,
		pollFunc:  pollFunc,
	}
}

// start begins polling the feed at the configured interval with resilience features
func (p *feedPoller) start(ctx context.Context) {
	// Ensure we recover from any panics in the poller goroutine
	defer func() {
		if r := recover(); r != nil {
			p.log.Error.Printf("Recovered from panic in feed %s: %v", p.feedConfig.Id, r)
			// Restart the poller after a delay
			time.AfterFunc(30*time.Second, func() {
				p.start(ctx)
			})
		}
	}()

	interval, _ := time.ParseDuration(p.feedConfig.PollInterval)
	backoff := initialBackoff

	// Initial poll
	p.pollWithRecovery(ctx)

	for {
		select {
		case <-ctx.Done():
			p.log.Info.Printf("Stopping poller for feed %s", p.feedConfig.Id)
			return
		case <-time.After(interval):
			if p.isCircuitOpen() {
				p.log.Error.Printf("Circuit open for feed %s, waiting to retry...", p.feedConfig.Id)
				time.Sleep(backoff)
				// Exponential backoff with jitter
				backoff = time.Duration(float64(backoff) * backoffMultiplier)
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}

			if err := p.pollWithRecovery(ctx); err != nil {
				p.recordFailure()
			} else {
				p.resetCircuit()
				// Reset backoff on successful poll
				backoff = initialBackoff
			}
		}
	}
}

// pollWithRecovery wraps pollOnce with panic recovery and logging
func (p *feedPoller) pollWithRecovery(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error.Printf("Recovered from panic during poll of %s: %v", p.feedConfig.Id, r)
		}
	}()

	p.log.Debug.Printf("Polling feed %s", p.feedConfig.Id)
	return p.pollFunc(ctx)
}

// isCircuitOpen checks if the circuit breaker should be open
func (p *feedPoller) isCircuitOpen() bool {
	if atomic.LoadInt32(&p.failureCount) < maxFailures {
		return false
	}

	// After resetTimeout, try again
	if time.Since(p.lastFailure) > resetTimeout {
		atomic.StoreInt32(&p.failureCount, 0)
		return false
	}

	return true
}

// recordFailure increments the failure count and updates the last failure time
func (p *feedPoller) recordFailure() {
	atomic.AddInt32(&p.failureCount, 1)
	p.lastFailure = time.Now()
}

// resetCircuit resets the circuit breaker
func (p *feedPoller) resetCircuit() {
	atomic.StoreInt32(&p.failureCount, 0)
}

// pollOnce performs a single poll of the feed with context timeout
func (p *feedPoller) pollOnce(ctx context.Context) error {
	// Add a timeout to the poll operation
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	return p.pollFunc(ctx)
}
