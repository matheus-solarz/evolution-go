package chathistory_repository

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	chathistory_model "github.com/EvolutionAPI/evolution-go/pkg/chathistory/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ChatHistoryRepository interface {
	UpsertMessage(msg *chathistory_model.ChatMessage) error
	UpsertMessagesBatch(msgs []chathistory_model.ChatMessage) error
	UpsertContact(contact *chathistory_model.ChatContact) error
	TouchContactLastMessage(instanceID, jid string, ts time.Time, isGroup bool) error
	UpdateContactPicture(instanceID, jid, url string) error
	MarkPictureFetched(instanceID, jid, url string) error
	EnrichExistingContacts(instanceID string, names map[string]ContactNamePatch) error
	ListLIDContactsWithoutName(instanceID string) ([]string, error)
	ListContactsNeedingPicture(instanceID string, limit int, maxAge time.Duration) ([]string, error)
	PurgeInstanceData(instanceID string) (deletedMessages int64, deletedContacts int64, err error)

	ListContacts(filter ContactFilter) ([]chathistory_model.ChatContact, string, error)
	ListMessages(filter MessageFilter) ([]chathistory_model.ChatMessage, string, error)
	ListChatsImportPage(filter ImportFilter) ([]chathistory_model.ChatContact, string, error)
	ListMessagesForChats(instanceID string, chatJIDs []string, since *time.Time, limitPerChat int) (map[string][]chathistory_model.ChatMessage, error)
}

type ImportFilter struct {
	InstanceID string
	IsGroup    *bool
	Since      *time.Time // contact must have last_message_at >= since
	PageSize   int
	Cursor     string // base64 of "<unix_nanos>|<jid>"
}

type ContactNamePatch struct {
	PushName     string
	FullName     string
	BusinessName string
}

type ContactFilter struct {
	InstanceID string
	Search     string
	IsGroup    *bool
	Since      *time.Time
	Limit      int
	Cursor     string // base64(last_message_at|id)
}

type MessageFilter struct {
	InstanceID  string
	ChatJID     string
	Since       *time.Time
	Until       *time.Time
	FromMe      *bool
	MessageType string
	Order       string // asc | desc (default desc)
	Limit       int
	Cursor      string // base64(timestamp|id)
}

type chatHistoryRepository struct {
	db *gorm.DB
}

func NewChatHistoryRepository(db *gorm.DB) ChatHistoryRepository {
	return &chatHistoryRepository{db: db}
}

func (r *chatHistoryRepository) UpsertMessage(msg *chathistory_model.ChatMessage) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "instance_id"},
			{Name: "chat_jid"},
			{Name: "message_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"sender_jid", "push_name", "from_me", "is_group",
			"message_type", "content", "media_url", "media_mime", "quoted_id",
			"timestamp", "source", "updated_at",
		}),
	}).Create(msg).Error
}

func (r *chatHistoryRepository) UpsertMessagesBatch(msgs []chathistory_model.ChatMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "instance_id"},
			{Name: "chat_jid"},
			{Name: "message_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"sender_jid", "push_name", "from_me", "is_group",
			"message_type", "content", "media_url", "media_mime", "quoted_id",
			"timestamp", "source", "updated_at",
		}),
	}).CreateInBatches(msgs, 200).Error
}

func (r *chatHistoryRepository) UpsertContact(contact *chathistory_model.ChatContact) error {
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "instance_id"},
			{Name: "jid"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"is_group", "push_name", "full_name", "business_name",
			"last_message_at", "unread_count", "updated_at",
		}),
	}).Create(contact).Error
}

func (r *chatHistoryRepository) TouchContactLastMessage(instanceID, jid string, ts time.Time, isGroup bool) error {
	contact := chathistory_model.ChatContact{
		InstanceID:    instanceID,
		JID:           jid,
		IsGroup:       isGroup,
		LastMessageAt: &ts,
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}, {Name: "jid"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"last_message_at": gorm.Expr("CASE WHEN chat_contacts.last_message_at IS NULL OR chat_contacts.last_message_at < ? THEN ? ELSE chat_contacts.last_message_at END", ts, ts),
			"is_group":        isGroup,
			"updated_at":      time.Now(),
		}),
	}).Create(&contact).Error
}

// EnrichExistingContacts updates push_name / full_name / business_name on contacts that ALREADY exist
// for the given instance. It never creates rows. Empty patch fields are ignored so a UPDATE never
// blanks out a previously-known name.
func (r *chatHistoryRepository) EnrichExistingContacts(instanceID string, names map[string]ContactNamePatch) error {
	if len(names) == 0 {
		return nil
	}
	now := time.Now()
	return r.db.Transaction(func(tx *gorm.DB) error {
		for jid, patch := range names {
			updates := map[string]interface{}{}
			if patch.PushName != "" {
				updates["push_name"] = patch.PushName
			}
			if patch.FullName != "" {
				updates["full_name"] = patch.FullName
			}
			if patch.BusinessName != "" {
				updates["business_name"] = patch.BusinessName
			}
			if len(updates) == 0 {
				continue
			}
			updates["updated_at"] = now
			if err := tx.Model(&chathistory_model.ChatContact{}).
				Where("instance_id = ? AND jid = ?", instanceID, jid).
				Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListLIDContactsWithoutName returns the JIDs of @lid contacts that still have empty
// push_name AND empty full_name — the candidates for LID→phone enrichment.
func (r *chatHistoryRepository) ListLIDContactsWithoutName(instanceID string) ([]string, error) {
	var jids []string
	err := r.db.Model(&chathistory_model.ChatContact{}).
		Where("instance_id = ?", instanceID).
		Where("jid LIKE ?", "%@lid").
		Where("(push_name IS NULL OR push_name = '') AND (full_name IS NULL OR full_name = '')").
		Pluck("jid", &jids).Error
	return jids, err
}

// PurgeInstanceData removes every chat_messages and chat_contacts row tied to the
// given instance_id. Called when an instance is deleted so we don't leak data across
// re-creations of an instance with a new id.
func (r *chatHistoryRepository) PurgeInstanceData(instanceID string) (int64, int64, error) {
	if instanceID == "" {
		return 0, 0, fmt.Errorf("instanceID required")
	}
	mRes := r.db.Where("instance_id = ?", instanceID).Delete(&chathistory_model.ChatMessage{})
	if mRes.Error != nil {
		return 0, 0, mRes.Error
	}
	cRes := r.db.Where("instance_id = ?", instanceID).Delete(&chathistory_model.ChatContact{})
	if cRes.Error != nil {
		return mRes.RowsAffected, 0, cRes.Error
	}
	return mRes.RowsAffected, cRes.RowsAffected, nil
}

func (r *chatHistoryRepository) UpdateContactPicture(instanceID, jid, url string) error {
	now := time.Now()
	return r.db.Model(&chathistory_model.ChatContact{}).
		Where("instance_id = ? AND jid = ?", instanceID, jid).
		Updates(map[string]interface{}{
			"picture_url":        url,
			"picture_fetched_at": now,
			"updated_at":         now,
		}).Error
}

// MarkPictureFetched stamps picture_fetched_at even when url is empty (no picture available),
// so the next refresh sweep skips this contact for at least the cache TTL window.
func (r *chatHistoryRepository) MarkPictureFetched(instanceID, jid, url string) error {
	now := time.Now()
	return r.db.Model(&chathistory_model.ChatContact{}).
		Where("instance_id = ? AND jid = ?", instanceID, jid).
		Updates(map[string]interface{}{
			"picture_url":        url,
			"picture_fetched_at": now,
			"updated_at":         now,
		}).Error
}

// ListContactsNeedingPicture returns JIDs of contacts whose picture cache is empty or older
// than maxAge, ordered by recent activity so the most relevant chats get pictures first.
func (r *chatHistoryRepository) ListContactsNeedingPicture(instanceID string, limit int, maxAge time.Duration) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().Add(-maxAge)
	var jids []string
	err := r.db.Model(&chathistory_model.ChatContact{}).
		Where("instance_id = ?", instanceID).
		Where("picture_fetched_at IS NULL OR picture_fetched_at < ?", cutoff).
		Order("last_message_at DESC NULLS LAST").
		Limit(limit).
		Pluck("jid", &jids).Error
	return jids, err
}

func (r *chatHistoryRepository) ListContacts(f ContactFilter) ([]chathistory_model.ChatContact, string, error) {
	q := r.db.Model(&chathistory_model.ChatContact{}).Where("instance_id = ?", f.InstanceID)

	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("push_name ILIKE ? OR full_name ILIKE ? OR jid ILIKE ?", like, like, like)
	}
	if f.IsGroup != nil {
		q = q.Where("is_group = ?", *f.IsGroup)
	}
	if f.Since != nil {
		q = q.Where("last_message_at >= ?", *f.Since)
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var rows []chathistory_model.ChatContact
	if err := q.Order("last_message_at DESC NULLS LAST, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
		// keep simple: client paginates by `since`/last_message_at; add encoded cursor in a future iteration
	}
	return rows, nextCursor, nil
}

// ListChatsImportPage returns one page of chat_contacts ordered by last_message_at DESC,
// using cursor-based pagination keyed by (last_message_at, jid). Excludes contacts that
// have no messages (last_message_at IS NULL) — they shouldn't show up in an import flow.
func (r *chatHistoryRepository) ListChatsImportPage(f ImportFilter) ([]chathistory_model.ChatContact, string, error) {
	q := r.db.Model(&chathistory_model.ChatContact{}).
		Where("instance_id = ?", f.InstanceID).
		Where("last_message_at IS NOT NULL")

	if f.IsGroup != nil {
		q = q.Where("is_group = ?", *f.IsGroup)
	}
	if f.Since != nil {
		q = q.Where("last_message_at >= ?", *f.Since)
	}

	if f.Cursor != "" {
		ts, jid, err := decodeImportCursor(f.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor: %w", err)
		}
		// Strict less-than (timestamp, jid) for DESC ordering
		q = q.Where("(last_message_at < ?) OR (last_message_at = ? AND jid > ?)", ts, ts, jid)
	}

	pageSize := f.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}

	var rows []chathistory_model.ChatContact
	if err := q.Order("last_message_at DESC, jid ASC").Limit(pageSize + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		if last.LastMessageAt != nil {
			nextCursor = encodeImportCursor(*last.LastMessageAt, last.JID)
		}
		rows = rows[:pageSize]
	}
	return rows, nextCursor, nil
}

// ListMessagesForChats fetches up to limitPerChat messages for each chat JID in one query
// using a window function. Returns map[chat_jid] -> ordered (asc) message slice.
func (r *chatHistoryRepository) ListMessagesForChats(instanceID string, chatJIDs []string, since *time.Time, limitPerChat int) (map[string][]chathistory_model.ChatMessage, error) {
	if len(chatJIDs) == 0 {
		return map[string][]chathistory_model.ChatMessage{}, nil
	}
	if limitPerChat <= 0 || limitPerChat > 1000 {
		limitPerChat = 200
	}

	// Use ROW_NUMBER() partitioned per chat to slice per-chat without N queries.
	args := []interface{}{instanceID}
	whereSince := ""
	if since != nil {
		whereSince = " AND timestamp >= ?"
		args = append(args, *since)
	}
	args = append(args, chatJIDs, limitPerChat)

	sql := `
WITH ranked AS (
  SELECT *, ROW_NUMBER() OVER (PARTITION BY chat_jid ORDER BY timestamp DESC, id DESC) AS rn
    FROM chat_messages
   WHERE instance_id = ?` + whereSince + ` AND chat_jid IN ?
)
SELECT * FROM ranked WHERE rn <= ? ORDER BY chat_jid, timestamp ASC, id ASC
`

	var rows []chathistory_model.ChatMessage
	if err := r.db.Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make(map[string][]chathistory_model.ChatMessage, len(chatJIDs))
	for _, m := range rows {
		out[m.ChatJID] = append(out[m.ChatJID], m)
	}
	return out, nil
}

func encodeImportCursor(ts time.Time, jid string) string {
	raw := strconv.FormatInt(ts.UnixNano(), 10) + "|" + jid
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

func decodeImportCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(0, nanos), parts[1], nil
}

func (r *chatHistoryRepository) ListMessages(f MessageFilter) ([]chathistory_model.ChatMessage, string, error) {
	q := r.db.Model(&chathistory_model.ChatMessage{}).Where("instance_id = ?", f.InstanceID)

	if f.ChatJID != "" {
		q = q.Where("chat_jid = ?", f.ChatJID)
	}
	if f.Since != nil {
		q = q.Where("timestamp >= ?", *f.Since)
	}
	if f.Until != nil {
		q = q.Where("timestamp <= ?", *f.Until)
	}
	if f.FromMe != nil {
		q = q.Where("from_me = ?", *f.FromMe)
	}
	if f.MessageType != "" {
		q = q.Where("message_type = ?", f.MessageType)
	}

	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	order := "DESC"
	if f.Order == "asc" {
		order = "ASC"
	}

	var rows []chathistory_model.ChatMessage
	if err := q.Order("timestamp " + order + ", id " + order).Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nextCursor, nil
}
