package store

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
	modelcfg "vista/internal/model"
)

// These samples survive conversation deletion, but share global usage retention
// and reset. Old ranking samples had neither connection nor route identity.
type callUsageRow struct {
	modelcfg.CallUsage `gorm:"embedded"`
	At                 int64
}

func (callUsageRow) TableName() string { return "connection_usage" }

func (s *Store) RecordCallUsage(ctx context.Context, sample modelcfg.CallUsage, now time.Time) (bool, error) {
	_, idErr := uuid.Parse(sample.ID)
	_, routeErr := hex.DecodeString(sample.Route)
	if idErr != nil || routeErr != nil || len(sample.Route) != 64 || sample.Connection == "" || len(sample.Connection) > 1024 || sample.Model == "" || len(sample.Model) > 512 || len(sample.ProxyID) > 128 {
		return false, errors.New("invalid model call sample identity")
	}
	if !slices.Contains([]string{"succeeded", "limited", "upstream", "rejected", "connection", "failed", "cancelled"}, sample.Outcome) {
		return false, errors.New("invalid model call outcome")
	}
	for _, tier := range []string{sample.RequestedTier, sample.ActualTier} {
		if !slices.Contains([]string{"", "standard", "fast", "priority", "ultrafast", "flex", "performance", "reserved", "scale"}, tier) {
			return false, errors.New("invalid model call tier")
		}
	}
	for _, n := range []*float64{sample.FirstTokenMS, sample.ConnectMS, sample.TLSMS, sample.OutputTPS} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0) {
			return false, errors.New("invalid model call measurement")
		}
	}
	row := callUsageRow{CallUsage: sample, At: now.Unix()}
	result := s.orm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	return result.RowsAffected > 0, result.Error
}

type ConnectionUsageGroup struct {
	Model             string   `json:"model"`
	ProxyID           string   `json:"proxyId"`
	Route             string   `json:"route"`
	RequestedTier     string   `json:"requestedTier"`
	ActualTier        string   `json:"actualTier"`
	Outcome           string   `json:"outcome"`
	Requests          int      `json:"requests"`
	FirstTokenMS      *float64 `json:"firstTokenMs"`
	FirstTokenSamples int      `json:"firstTokenSamples"`
	ConnectMS         *float64 `json:"connectMs"`
	ConnectSamples    int      `json:"connectSamples"`
	TLSMS             *float64 `json:"tlsMs"`
	TLSSamples        int      `json:"tlsSamples"`
	OutputTPS         *float64 `json:"outputTps"`
	OutputSamples     int      `json:"outputSamples"`
}

func (s *Store) ConnectionUsage(ctx context.Context, connection string, days int, now time.Time) ([]ConnectionUsageGroup, bool, error) {
	groups := []ConnectionUsageGroup{}
	err := s.orm.WithContext(ctx).Raw(`SELECT model,proxy_id,route,requested_tier,actual_tier,outcome,
  COUNT(*) requests, AVG(first_token_ms) first_token_ms, COUNT(first_token_ms) first_token_samples,
  AVG(connect_ms) connect_ms, COUNT(connect_ms) connect_samples, AVG(tls_ms) tls_ms, COUNT(tls_ms) tls_samples,
  AVG(output_tps) output_tps, COUNT(output_tps) output_samples
  FROM connection_usage WHERE connection=? AND at>=?
  GROUP BY model,proxy_id,route,requested_tier,actual_tier,outcome ORDER BY MAX(at) DESC,model,route,requested_tier,actual_tier,outcome LIMIT 513`, connection, now.AddDate(0, 0, -days).Unix()).Scan(&groups).Error
	if len(groups) > 512 {
		return groups[:512], true, err
	}
	return groups, false, err
}
