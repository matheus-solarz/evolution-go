package chathistory_model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ChatContact struct {
	Id                string     `json:"id" gorm:"type:uuid;primaryKey"`
	InstanceID        string     `json:"instance_id" gorm:"type:varchar(64);not null;uniqueIndex:ux_chat_contacts_dedup,priority:1;index:ix_chat_contacts_instance_last,priority:1"`
	JID               string     `json:"jid" gorm:"column:jid;type:varchar(128);not null;uniqueIndex:ux_chat_contacts_dedup,priority:2"`
	IsGroup           bool       `json:"is_group" gorm:"index"`
	PushName          string     `json:"push_name"`
	FullName          string     `json:"full_name"`
	BusinessName      string     `json:"business_name,omitempty"`
	PictureURL        string     `json:"picture_url,omitempty" gorm:"type:text"`
	PictureFetchedAt  *time.Time `json:"picture_fetched_at,omitempty"`
	LastMessageAt     *time.Time `json:"last_message_at,omitempty" gorm:"index:ix_chat_contacts_instance_last,priority:2,sort:desc"`
	UnreadCount       int        `json:"unread_count"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (c *ChatContact) TableName() string {
	return "chat_contacts"
}

func (c *ChatContact) BeforeCreate(tx *gorm.DB) (err error) {
	if c.Id == "" {
		c.Id = uuid.New().String()
	}
	return
}
