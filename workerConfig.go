package configs

import "time"

type WorkerConfig struct {
	QueueName   string
	WorkerType  string
	RetryCount  int
	RetryDelay  time.Duration
	Concurrency int
	SLADeadline time.Duration
}
