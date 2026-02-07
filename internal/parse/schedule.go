package parse

import "time"

const maxDelay = 24 * time.Hour

type ScheduleDirective struct {
	Delay   time.Duration
	At      time.Time
	Content string
	Memory  []string
}
