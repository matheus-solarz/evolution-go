package chathistory_model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ChatMessage struct {
	Id          string    `json:"id" gorm:"type:uuid;primaryKey"`
	InstanceID  string    `json:"instance_id" gorm:"type:varchar(64);not null;uniqueIndex:ux_chat_messages_dedup,priority:1;index:ix_chat_messages_instance_ts,priority:1"`
	ChatJID     string    `json:"chat_jid" gorm:"column:chat_jid;type:varchar(128);not null;uniqueIndex:ux_chat_messages_dedup,priority:2;index:ix_chat_messages_chat_ts,priority:1"`
	MessageID   string    `json:"message_id" gorm:"column:message_id;type:varchar(128);not null;uniqueIndex:ux_chat_messages_dedup,priority:3"`
	SenderJID   string    `json:"sender_jid" gorm:"column:sender_jid;type:varchar(128);index"`
	PushName    string    `json:"push_name"`
	FromMe      bool      `json:"from_me" gorm:"index"`
	IsGroup     bool      `json:"is_group"`
	MessageType string    `json:"message_type" gorm:"type:varchar(32);index"`
	Content     string    `json:"content" gorm:"type:text"`
	MediaURL    string    `json:"media_url,omitempty" gorm:"type:text"`
	MediaMime   string    `json:"media_mime,omitempty" gorm:"type:varchar(128)"`
	QuotedID    string    `json:"quoted_id,omitempty" gorm:"type:varchar(128);index"`
	Timestamp   time.Time `json:"timestamp" gorm:"not null;index:ix_chat_messages_instance_ts,priority:2,sort:desc;index:ix_chat_messages_chat_ts,priority:2,sort:desc"`
	Source      string    `json:"source" gorm:"type:varchar(32)"` // history_sync | realtime | outgoing
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (m *ChatMessage) TableName() string {
	return "chat_messages"
}

func (m *ChatMessage) BeforeCreate(tx *gorm.DB) (err error) {
	if m.Id == "" {
		m.Id = uuid.New().String()
	}
	return
}
