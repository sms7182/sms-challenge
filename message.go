package models

import (
	"time"
)

type Message struct {
	ID           string    `gorm:"type:uuid;primaryKey;" json:"id"`
	ClientID     string    `gorm:"not null" json:"client_id"`
	Type         string    `gorm:"type:varchar(10);check:type IN ('normal','express')" json:"type"`
	ToNumber     string    `gorm:"type:varchar(20)" json:"to_number"`
	FromNumber   string    `gorm:"type:varchar(20)" json:"from_number"`
	Content      string    `gorm:"type:text" json:"content"`
	Status       string    `gorm:"type:varchar(20);check:status IN ('queued','sent','failed','delivered','expired')" json:"status"`
	ErrorMessage *string   `gorm:"type:text" json:"error_message,omitempty"`
	QueuedAt     time.Time `gorm:"autoCreateTime" json:"queued_at"`
	UpdatedAt    time.Time `gorm:"autoUpdateTime" json:"updated_at,omitempty"`
}
