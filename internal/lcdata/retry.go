package lcdata

import (
	"context"
	"math/rand/v2"
	"time"
)

// withRetry runs fn up to 1+retryCount times with exponential backoff + jitter.
// baseDelay is the initial wait between attempts (e.g. "1s").
// Each attempt doubles the delay; jitter adds up to 25% random variance.
// onRetry is called before each retry attempt (may be nil).
func withRetry(
	ctx context.Context,
	retryCount int,
	baseDelay string,
	onRetry func(attempt int, err error),
	fn func() (map[string]any, error),
) (map[string]any, error) {

	delay := time.Second
	if baseDelay != "" {
		if d, err := time.ParseDuration(baseDelay); err == nil {
			delay = d
		}
	}

	var lastErr error
	for attempt := 0; attempt <= retryCount; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err

		if attempt < retryCount {
			if onRetry != nil {
				onRetry(attempt+1, err)
			}
			// Exponential backoff with ±25% jitter
			jitter := time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(jitter):
			}
			delay *= 2
		}
	}
	return nil, lastErr
}
