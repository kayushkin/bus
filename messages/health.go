package messages

import "time"

// HealthReport is published by services to report their health status.
type HealthReport struct {
	Service   string    `json:"service"`
	Status    string    `json:"status"`
	Uptime    int64     `json:"uptime"`
	Timestamp time.Time `json:"timestamp"`
}
