// Package middleware provides HTTP middleware functions for the application.
package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/verigate/verigate-server/internal/pkg/utils/errors"

	"github.com/gin-gonic/gin"
)

// RedisRateLimiter implements a sliding window rate limiting algorithm using Redis.
// It tracks and limits the number of requests per client within a specified time window.
type RedisRateLimiter struct {
	client      *redis.Client
	keyPrefix   string
	limitPerMin int
	window      time.Duration
}

// NewRedisRateLimiter creates a new rate limiter instance.
// Parameters:
// - client: Redis client for storing rate limit data
// - keyPrefix: Prefix for Redis keys to prevent collisions with other data
// - limitPerMin: Maximum number of requests allowed per minute
// - window: Time window for rate limiting (e.g., 1 minute)
func NewRedisRateLimiter(client *redis.Client, keyPrefix string, limitPerMin int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		client:      client,
		keyPrefix:   keyPrefix,
		limitPerMin: limitPerMin,
		window:      window,
	}
}

// RateLimitMiddleware creates a Gin middleware that enforces rate limits.
// It uses a sliding window algorithm to count requests within a time window.
// The rate limit can be based on either the user ID (if authenticated) or the client IP.
// When a client exceeds the rate limit, the middleware responds with a 429 Too Many Requests error.
func RateLimitMiddleware(limiter *RedisRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := context.Background()

		// Create rate limit key based on IP or user ID
		var key string
		if userID, exists := c.Get("user_id"); exists {
			key = fmt.Sprintf("%suser:%v", limiter.keyPrefix, userID)
		} else {
			key = fmt.Sprintf("%sip:%s", limiter.keyPrefix, c.ClientIP())
		}

		// Use Redis sliding window algorithm
		now := time.Now().Unix()
		windowStart := now - int64(limiter.window.Seconds())

		pipe := limiter.client.Pipeline()

		// Remove old entries outside the window
		pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprintf("%d", windowStart))

		// Add current request
		pipe.ZAdd(ctx, key, &redis.Z{
			Score:  float64(now),
			Member: now,
		})

		// Count requests in window
		pipe.ZCard(ctx, key)

		// Set expiry
		pipe.Expire(ctx, key, limiter.window)

		results, err := pipe.Exec(ctx)
		if err != nil {
			// On error, allow the request
			c.Next()
			return
		}

		count := results[2].(*redis.IntCmd).Val()

		// Set rate limit headers
		c.Header("X-RateLimit-Limit", fmt.Sprintf("%d", limiter.limitPerMin))
		c.Header("X-RateLimit-Remaining", fmt.Sprintf("%d", max(0, limiter.limitPerMin-int(count))))
		c.Header("X-RateLimit-Reset", fmt.Sprintf("%d", now+int64(limiter.window.Seconds())))

		if count > int64(limiter.limitPerMin) {
			c.Error(errors.TooManyRequests(errors.ErrMsgRateLimitExceeded))
			c.Abort()
			return
		}

		c.Next()
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
