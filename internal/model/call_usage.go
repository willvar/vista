package model

// CallUsage is a logical inference call, including same-tier retries. It carries
// no addresses, credentials, prompts, or upstream error bodies.
type CallUsage struct {
	ID            string   `json:"id" gorm:"primaryKey"`
	Connection    string   `json:"connection"`
	Model         string   `json:"model"`
	ProxyID       string   `json:"proxyId"`
	Route         string   `json:"route"`
	RequestedTier string   `json:"requestedTier"`
	ActualTier    string   `json:"actualTier"`
	Outcome       string   `json:"outcome"`
	FirstTokenMS  *float64 `json:"firstTokenMs,omitempty"`
	ConnectMS     *float64 `json:"connectMs,omitempty"`
	TLSMS         *float64 `json:"tlsMs,omitempty"`
	OutputTPS     *float64 `json:"outputTps,omitempty"`
}

func (m Model) UsageConnection() string {
	if m.ConnectionID != "" {
		return "connection:" + m.ConnectionID
	}
	if m.Key != "" {
		return "model:" + m.Key
	}
	return ""
}
