package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"checkwallet/internal/platform/cache"
	"checkwallet/internal/provider"
)

// cacheEntry is the self-describing cached payload — the embedded status
// means a negative/degraded entry can never replay as a clean result (F5).
type cacheEntry struct {
	Status         string                 `json:"status"`
	Observations   []provider.Observation `json:"observations"`
	Signals        map[string]any         `json:"signals"`
	ProviderScore  *float64               `json:"provider_score,omitempty"`
	Exposures      []provider.Exposure    `json:"exposures"`
	Error          *ErrorInfo             `json:"error,omitempty"`
	HTTPStatus     int32                  `json:"http_status,omitempty"`
	LatencyMS      int64                  `json:"latency_ms,omitempty"`
	FetchedAt      time.Time              `json:"fetched_at"`
	CacheExpiresAt time.Time              `json:"cache_expires_at"`
	Persisted      bool                   `json:"persisted"`
}

var (
	errCacheMiss = errors.New("cache miss")
	errCacheDown = errors.New("cache down")
)

// get returns a decoded entry; errCacheMiss on absent/corrupt payloads,
// errCacheDown on Redis outages — callers treat every failure as a bypass,
// never fatal, but only an outage earns the visible "bypassed" marker.
func (s *Service) get(ctx context.Context, source, netCode, normalized string) (*cacheEntry, error) {
	if s.rdb == nil {
		return nil, errCacheDown
	}
	payload, err := s.rdb.Get(ctx, cache.Provider(source, netCode, normalized)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, errCacheMiss
		}
		if s.log != nil {
			s.log.Warn("provider cache read failed; bypassing",
				"source", source, "error", err)
		}
		return nil, errCacheDown
	}
	var e cacheEntry
	if json.Unmarshal(payload, &e) != nil {
		return nil, errCacheMiss
	}
	return &e, nil
}

// put writes the entry with the status-appropriate TTL — positive results
// live 15 m (spec §14); degraded/negative entries live 60 s so a recovering
// provider is not masked long (negative cache must never hide recovery).
func (s *Service) put(ctx context.Context, source, netCode, normalized string,
	e *cacheEntry) {

	if s.rdb == nil {
		return
	}
	ttl := s.posTTL
	if e.Status != "OK" {
		ttl = s.negTTL
	}
	e.CacheExpiresAt = time.Now().Add(ttl).UTC()
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := s.rdb.Set(ctx, cache.Provider(source, netCode, normalized),
		payload, ttl).Err(); err != nil && s.log != nil {
		s.log.Warn("provider cache write failed; continuing uncached",
			"source", source, "error", err)
	}
}

// refreshAllowed implements the F3 quota guard: SET NX EX grants one
// bypass per (source, net, addr) per cooldown. The second return reports
// a Redis outage so callers can surface the "bypassed" marker even when
// the guard itself fails open — an outage must never be silent.
func (s *Service) refreshAllowed(ctx context.Context, source, netCode,
	normalized string, refresh bool) (granted, cacheDown bool) {

	if !refresh {
		return false, false
	}
	if s.rdb == nil {
		return true, true
	}
	ok, err := s.rdb.SetNX(ctx,
		cache.ProviderRefresh(source, netCode, normalized), 1, s.cooldown).Result()
	if err != nil {
		if s.log != nil {
			s.log.Warn("refresh guard check failed; failing open",
				"source", source, "error", err)
		}
		return true, true
	}
	return ok, false
}
