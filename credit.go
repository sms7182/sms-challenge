package models

import (
	"time"
)

type Credit struct {
	ClientID  string    `gorm:"primaryKey;not null" json:"client_id"`
	Balance   int       `gorm:"not null;default:0" json:"balance"`
	Name      string    `gorm:"type:varchar(50);not null" json:"name"`
	UpdatedAt time.Time `gorm:"autoUpdateTime" json:"updated_at"`
}
