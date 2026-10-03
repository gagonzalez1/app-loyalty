package middleware

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func CORS() gin.HandlerFunc {
	config := cors.Config{
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "If-Match", "Idempotency-Key", "X-Client-Platform", "X-Demo-Access-Code", "X-Request-ID", "Cache-Control", "Last-Event-ID"},
		ExposeHeaders:    []string{"ETag", "Idempotent-Replayed", "Retry-After", "X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	origins := os.Getenv("CORS_ORIGINS")
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production")
	if origins == "" {
		if production {
			panic("CORS_ORIGINS is required in production")
		}
		return func(c *gin.Context) { c.Next() }
	}
	for origin := range strings.SplitSeq(origins, ",") {
		trimmed := strings.TrimSpace(origin)
		parsed, err := url.Parse(trimmed)
		if trimmed == "" || trimmed == "*" || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			panic(fmt.Sprintf("invalid explicit CORS origin %q", trimmed))
		}
		if production && parsed.Scheme != "https" {
			panic(fmt.Sprintf("production CORS origin must use https: %q", trimmed))
		}
		config.AllowOrigins = append(config.AllowOrigins, trimmed)
	}

	return cors.New(config)
}
