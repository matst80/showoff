package main

import "time"

// Stats represents current server stats for dashboards & API.
type Stats struct {
	Clients      int    `json:"clients"`
	Pending      int    `json:"pending"`
	TotalTunnels int64  `json:"total_tunnels"`
	Timeouts     int64  `json:"timeouts"`
	Now          string `json:"now"`
}

func collectStats(s StateStore) Stats {
	c, p, total, timeouts := s.getStats()
	return Stats{Clients: c, Pending: p, TotalTunnels: total, Timeouts: timeouts, Now: time.Now().UTC().Format(time.RFC3339)}
}

// ToTemplateMap returns a map suited for html/template rendering with expected capitalized keys.
func (s Stats) ToTemplateMap() map[string]any {
	return map[string]any{
		"Clients":  s.Clients,
		"Pending":  s.Pending,
		"Total":    s.TotalTunnels,
		"Timeouts": s.Timeouts,
	}
}
