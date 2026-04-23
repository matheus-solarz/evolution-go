package chathistory_service

import (
	"strings"
	"time"

	chathistory_extractor "github.com/EvolutionAPI/evolution-go/pkg/chathistory/extractor"
	chathistory_model "github.com/EvolutionAPI/evolution-go/pkg/chathistory/model"
	chathistory_repository "github.com/EvolutionAPI/evolution-go/pkg/chathistory/repository"
	logger_wrapper "github.com/EvolutionAPI/evolution-go/pkg/logger"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ContactNamePatch is the public shape callers use to enrich contacts (no whatsmeow types leak out).
type ContactNamePatch = chathistory_repository.ContactNamePatch

type ChatHistoryService interface {
	RecordHistorySync(instanceID string, evt *events.HistorySync)
	RecordRealtime(instanceID string, evt *events.Message)
	RecordOutgoing(instanceID string, info types.MessageInfo, msg *waE2E.Message)
	EnrichContactNames(instanceID string, names map[string]ContactNamePatch) error
	ListLIDContactsWithoutName(instanceID string) ([]string, error)
	ListContactsNeedingPicture(instanceID string, limit int, maxAge time.Duration) ([]string, error)
	MarkPictureFetched(instanceID, jid, url string) error
	PurgeInstanceData(instanceID string) (deletedMessages int64, deletedContacts int64, err error)
	ListContacts(filter chathistory_repository.ContactFilter) ([]chathistory_model.ChatContact, string, error)
	ListMessages(filter chathistory_repository.MessageFilter) ([]chathistory_model.ChatMessage, string, error)
	GetImportPage(filter chathistory_repository.ImportFilter, since *time.Time, messagesPerChat int) (*ImportPage, error)
}

type ImportContact struct {
	JID              string     `json:"jid"`
	Name             string     `json:"name"`
	IsGroup          bool       `json:"is_group"`
	PictureURL       string     `json:"picture_url"`
	PictureFetchedAt *time.Time `json:"picture_fetched_at,omitempty"`
	LastMessageAt    *time.Time `json:"last_message_at,omitempty"`
}

type ImportItem struct {
	Contact  ImportContact                  `json:"contact"`
	Messages []chathistory_model.ChatMessage `json:"messages"`
}

type ImportPage struct {
	Items      []ImportItem `json:"data"`
	NextCursor string       `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}

type chatHistoryService struct {
	repo          chathistory_repository.ChatHistoryRepository
	loggerWrapper *logger_wrapper.LoggerManager
}

func NewChatHistoryService(repo chathistory_repository.ChatHistoryRepository, loggerWrapper *logger_wrapper.LoggerManager) ChatHistoryService {
	return &chatHistoryService{repo: repo, loggerWrapper: loggerWrapper}
}

func (s *chatHistoryService) ListContacts(f chathistory_repository.ContactFilter) ([]chathistory_model.ChatContact, string, error) {
	return s.repo.ListContacts(f)
}

func (s *chatHistoryService) ListMessages(f chathistory_repository.MessageFilter) ([]chathistory_model.ChatMessage, string, error) {
	return s.repo.ListMessages(f)
}

func (s *chatHistoryService) EnrichContactNames(instanceID string, names map[string]ContactNamePatch) error {
	return s.repo.EnrichExistingContacts(instanceID, names)
}

func (s *chatHistoryService) ListLIDContactsWithoutName(instanceID string) ([]string, error) {
	return s.repo.ListLIDContactsWithoutName(instanceID)
}

func (s *chatHistoryService) ListContactsNeedingPicture(instanceID string, limit int, maxAge time.Duration) ([]string, error) {
	return s.repo.ListContactsNeedingPicture(instanceID, limit, maxAge)
}

func (s *chatHistoryService) MarkPictureFetched(instanceID, jid, url string) error {
	return s.repo.MarkPictureFetched(instanceID, jid, url)
}

func (s *chatHistoryService) PurgeInstanceData(instanceID string) (int64, int64, error) {
	return s.repo.PurgeInstanceData(instanceID)
}

// GetImportPage returns one paginated batch of chats with their messages, shaped for
// the Chatwoot connector's bulk import flow.
func (s *chatHistoryService) GetImportPage(filter chathistory_repository.ImportFilter, since *time.Time, messagesPerChat int) (*ImportPage, error) {
	chats, nextCursor, err := s.repo.ListChatsImportPage(filter)
	if err != nil {
		return nil, err
	}
	if len(chats) == 0 {
		return &ImportPage{Items: []ImportItem{}, HasMore: false}, nil
	}

	chatJIDs := make([]string, 0, len(chats))
	for _, c := range chats {
		chatJIDs = append(chatJIDs, c.JID)
	}

	msgs, err := s.repo.ListMessagesForChats(filter.InstanceID, chatJIDs, since, messagesPerChat)
	if err != nil {
		return nil, err
	}

	items := make([]ImportItem, 0, len(chats))
	for _, c := range chats {
		items = append(items, ImportItem{
			Contact: ImportContact{
				JID:              c.JID,
				Name:             pickContactName(c),
				IsGroup:          c.IsGroup,
				PictureURL:       c.PictureURL,
				PictureFetchedAt: c.PictureFetchedAt,
				LastMessageAt:    c.LastMessageAt,
			},
			Messages: msgs[c.JID],
		})
	}

	return &ImportPage{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    nextCursor != "",
	}, nil
}

func pickContactName(c chathistory_model.ChatContact) string {
	if c.FullName != "" {
		return c.FullName
	}
	if c.PushName != "" {
		return c.PushName
	}
	if c.BusinessName != "" {
		return c.BusinessName
	}
	return ""
}

// isExcluded returns true for JIDs that should never be persisted as contacts/messages.
// status@broadcast is the WhatsApp Stories feed and *@newsletter are WhatsApp Channels —
// neither maps to a Chatwoot conversation.
func isExcluded(jid string) bool {
	if jid == "" || jid == "status@broadcast" {
		return true
	}
	if strings.HasSuffix(jid, "@newsletter") {
		return true
	}
	return false
}

// RecordHistorySync persists Conversations + HistorySyncMsgs delivered during pairing or via
// on-demand BuildHistorySyncRequest. Contacts are only created when the conversation produced
// at least one persisted message — avoids polluting the table with phone-book noise that
// HistorySync emits via Pushnames[].
func (s *chatHistoryService) RecordHistorySync(instanceID string, evt *events.HistorySync) {
	if evt == nil || evt.Data == nil {
		return
	}
	log := s.loggerWrapper.GetLogger(instanceID)
	syncType := evt.Data.GetSyncType().String()

	// Pushnames are JID→displayName hints — keep them in memory so we can attach a name
	// to a contact only when that JID actually appears in a persisted message/conversation.
	pushnames := make(map[string]string, len(evt.Data.GetPushnames()))
	for _, pn := range evt.Data.GetPushnames() {
		jid := pn.GetID()
		name := pn.GetPushname()
		if jid == "" || name == "" {
			continue
		}
		pushnames[jid] = name
	}

	totalMsgs := 0
	chatsWithMsgs := 0
	skippedConvs := 0

	for _, conv := range evt.Data.GetConversations() {
		chatJID := conv.GetID()
		if isExcluded(chatJID) {
			skippedConvs++
			continue
		}
		isGroup := strings.HasSuffix(chatJID, "@g.us")

		batch := make([]chathistory_model.ChatMessage, 0, len(conv.GetMessages()))
		var latestTS time.Time

		for _, hsm := range conv.GetMessages() {
			row, ok := s.buildFromHistorySyncMsg(instanceID, chatJID, isGroup, hsm)
			if !ok {
				continue
			}
			if row.PushName == "" {
				if name, found := pushnames[row.SenderJID]; found {
					row.PushName = name
				}
			}
			batch = append(batch, row)
			if row.Timestamp.After(latestTS) {
				latestTS = row.Timestamp
			}
		}

		if len(batch) == 0 {
			skippedConvs++
			continue
		}

		if err := s.repo.UpsertMessagesBatch(batch); err != nil {
			log.LogError("[%s] history-sync upsert failed for %s: %v", instanceID, chatJID, err)
			continue
		}
		chatsWithMsgs++
		totalMsgs += len(batch)

		_ = s.repo.UpsertContact(&chathistory_model.ChatContact{
			InstanceID:  instanceID,
			JID:         chatJID,
			IsGroup:     isGroup,
			FullName:    conv.GetName(),
			PushName:    pushnames[chatJID],
			UnreadCount: int(conv.GetUnreadCount()),
		})

		if !latestTS.IsZero() {
			_ = s.repo.TouchContactLastMessage(instanceID, chatJID, latestTS, isGroup)
		}
	}

	log.LogInfo("[%s] HistorySync (%s) persisted: %d msgs across %d chats (%d empty/excluded conversations skipped)",
		instanceID, syncType, totalMsgs, chatsWithMsgs, skippedConvs)
}

func (s *chatHistoryService) buildFromHistorySyncMsg(instanceID, chatJID string, isGroup bool, hsm *waHistorySync.HistorySyncMsg) (chathistory_model.ChatMessage, bool) {
	wm := hsm.GetMessage()
	if wm == nil {
		return chathistory_model.ChatMessage{}, false
	}
	key := wm.GetKey()
	if key == nil || key.GetID() == "" {
		return chathistory_model.ChatMessage{}, false
	}

	ext := chathistory_extractor.Extract(wm.GetMessage())
	if ext.MessageType == "" {
		return chathistory_model.ChatMessage{}, false
	}

	ts := time.Unix(int64(wm.GetMessageTimestamp()), 0)

	senderJID := wm.GetParticipant()
	if senderJID == "" && !isGroup && !key.GetFromMe() {
		senderJID = chatJID
	}

	return chathistory_model.ChatMessage{
		InstanceID:  instanceID,
		ChatJID:     chatJID,
		MessageID:   key.GetID(),
		SenderJID:   senderJID,
		PushName:    wm.GetPushName(),
		FromMe:      key.GetFromMe(),
		IsGroup:     isGroup,
		MessageType: ext.MessageType,
		Content:     ext.Content,
		MediaURL:    ext.MediaURL,
		MediaMime:   ext.MediaMime,
		QuotedID:    ext.QuotedID,
		Timestamp:   ts,
		Source:      "history_sync",
	}, true
}

// RecordRealtime persists a single inbound message arriving via *events.Message.
func (s *chatHistoryService) RecordRealtime(instanceID string, evt *events.Message) {
	if evt == nil || evt.Message == nil {
		return
	}
	chatJID := evt.Info.Chat.String()
	if isExcluded(chatJID) {
		return
	}
	ext := chathistory_extractor.Extract(evt.Message)
	if ext.MessageType == "" {
		return
	}

	isGroup := evt.Info.IsGroup

	row := chathistory_model.ChatMessage{
		InstanceID:  instanceID,
		ChatJID:     chatJID,
		MessageID:   evt.Info.ID,
		SenderJID:   evt.Info.Sender.String(),
		PushName:    evt.Info.PushName,
		FromMe:      evt.Info.IsFromMe,
		IsGroup:     isGroup,
		MessageType: ext.MessageType,
		Content:     ext.Content,
		MediaURL:    ext.MediaURL,
		MediaMime:   ext.MediaMime,
		QuotedID:    ext.QuotedID,
		Timestamp:   evt.Info.Timestamp,
		Source:      "realtime",
	}

	if err := s.repo.UpsertMessage(&row); err != nil {
		s.loggerWrapper.GetLogger(instanceID).LogError("[%s] realtime upsert failed for %s/%s: %v", instanceID, chatJID, evt.Info.ID, err)
		return
	}
	_ = s.repo.TouchContactLastMessage(instanceID, chatJID, evt.Info.Timestamp, isGroup)
}

// RecordOutgoing persists a message sent through this API.
func (s *chatHistoryService) RecordOutgoing(instanceID string, info types.MessageInfo, msg *waE2E.Message) {
	chatJID := info.Chat.String()
	if isExcluded(chatJID) {
		return
	}
	ext := chathistory_extractor.Extract(msg)
	if ext.MessageType == "" {
		return
	}

	isGroup := strings.HasSuffix(chatJID, "@g.us")

	ts := info.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	row := chathistory_model.ChatMessage{
		InstanceID:  instanceID,
		ChatJID:     chatJID,
		MessageID:   info.ID,
		SenderJID:   info.Sender.String(),
		FromMe:      true,
		IsGroup:     isGroup,
		MessageType: ext.MessageType,
		Content:     ext.Content,
		MediaURL:    ext.MediaURL,
		MediaMime:   ext.MediaMime,
		QuotedID:    ext.QuotedID,
		Timestamp:   ts,
		Source:      "outgoing",
	}

	if err := s.repo.UpsertMessage(&row); err != nil {
		s.loggerWrapper.GetLogger(instanceID).LogError("[%s] outgoing upsert failed for %s/%s: %v", instanceID, chatJID, info.ID, err)
		return
	}
	_ = s.repo.TouchContactLastMessage(instanceID, chatJID, ts, isGroup)
}
