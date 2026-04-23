package chathistory_handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	chathistory_repository "github.com/EvolutionAPI/evolution-go/pkg/chathistory/repository"
	chathistory_service "github.com/EvolutionAPI/evolution-go/pkg/chathistory/service"
	instance_model "github.com/EvolutionAPI/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

type ChatHistoryHandler interface {
	ListContacts(ctx *gin.Context)
	ListMessages(ctx *gin.Context)
	Import(ctx *gin.Context)
}

type chatHistoryHandler struct {
	service chathistory_service.ChatHistoryService
}

func NewChatHistoryHandler(service chathistory_service.ChatHistoryService) ChatHistoryHandler {
	return &chatHistoryHandler{service: service}
}

// ListContacts retrieves contacts persisted from history sync + realtime traffic.
// @Summary List chat contacts
// @Description Returns contacts (1:1 + groups) seen by this instance, ordered by most recent activity.
// @Tags Chat
// @Produce json
// @Param search query string false "Substring match against push_name / full_name / jid"
// @Param is_group query bool false "Filter by group vs 1:1"
// @Param since query string false "RFC3339 timestamp; only contacts with last_message_at >= since"
// @Param limit query int false "Max rows (default 100, max 500)"
// @Success 200 {object} gin.H
// @Router /chat/contacts [get]
func (h *chatHistoryHandler) ListContacts(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	filter := chathistory_repository.ContactFilter{
		InstanceID: instance.Id,
		Search:     strings.TrimSpace(ctx.Query("search")),
		Limit:      parseIntDefault(ctx.Query("limit"), 100),
	}

	if v := ctx.Query("is_group"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid is_group: " + err.Error()})
			return
		}
		filter.IsGroup = &b
	}

	if v := ctx.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid since (expect RFC3339): " + err.Error()})
			return
		}
		filter.Since = &t
	}

	rows, _, err := h.service.ListContacts(filter)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": rows, "count": len(rows)})
}

// ListMessages retrieves persisted chat messages with filters.
// @Summary List chat messages
// @Description Returns messages persisted from history sync, realtime delivery and outgoing API calls.
// @Tags Chat
// @Produce json
// @Param chat_jid query string false "Filter by full chat JID (e.g. 5511999999999@s.whatsapp.net)"
// @Param since query string false "RFC3339 lower bound on timestamp"
// @Param until query string false "RFC3339 upper bound on timestamp"
// @Param months query int false "Convenience: only messages from the last N months (overrides since)"
// @Param from_me query bool false "Filter by direction"
// @Param message_type query string false "text | image | audio | video | document | sticker | reaction | contact | location"
// @Param order query string false "asc | desc (default desc)"
// @Param limit query int false "Max rows (default 100, max 1000)"
// @Success 200 {object} gin.H
// @Router /chat/messages [get]
func (h *chatHistoryHandler) ListMessages(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	filter := chathistory_repository.MessageFilter{
		InstanceID:  instance.Id,
		ChatJID:     strings.TrimSpace(ctx.Query("chat_jid")),
		MessageType: strings.TrimSpace(ctx.Query("message_type")),
		Order:       strings.ToLower(ctx.Query("order")),
		Limit:       parseIntDefault(ctx.Query("limit"), 100),
	}

	if v := ctx.Query("months"); v != "" {
		months, err := strconv.Atoi(v)
		if err != nil || months <= 0 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid months: must be positive integer"})
			return
		}
		t := time.Now().AddDate(0, -months, 0)
		filter.Since = &t
	} else if v := ctx.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid since (expect RFC3339): " + err.Error()})
			return
		}
		filter.Since = &t
	}

	if v := ctx.Query("until"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid until (expect RFC3339): " + err.Error()})
			return
		}
		filter.Until = &t
	}

	if v := ctx.Query("from_me"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid from_me: " + err.Error()})
			return
		}
		filter.FromMe = &b
	}

	rows, _, err := h.service.ListMessages(filter)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": rows, "count": len(rows)})
}

// Import returns chats grouped with their messages, shaped for the Chatwoot connector.
// Each item: { contact: { jid, name, picture_url, is_group, last_message_at }, messages: [...] }.
// @Summary Bulk import chats with messages
// @Description Returns chats with name + photo + nested messages, paginated via opaque cursor.
// @Tags Chat
// @Produce json
// @Param months query int false "Convenience: only chats with messages in the last N months"
// @Param since query string false "RFC3339 lower bound on last_message_at (alternative to months)"
// @Param is_group query bool false "Filter by group vs 1:1"
// @Param page_size query int false "Chats per page (default 50, max 200)"
// @Param messages_per_chat query int false "Max messages per chat (default 200, max 1000)"
// @Param cursor query string false "Opaque pagination cursor returned by previous call"
// @Success 200 {object} gin.H
// @Router /chat/import [get]
func (h *chatHistoryHandler) Import(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	filter := chathistory_repository.ImportFilter{
		InstanceID: instance.Id,
		PageSize:   parseIntDefault(ctx.Query("page_size"), 50),
		Cursor:     strings.TrimSpace(ctx.Query("cursor")),
	}

	var since *time.Time
	if v := ctx.Query("months"); v != "" {
		months, err := strconv.Atoi(v)
		if err != nil || months <= 0 {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid months: must be positive integer"})
			return
		}
		t := time.Now().AddDate(0, -months, 0)
		since = &t
		filter.Since = &t
	} else if v := ctx.Query("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid since (expect RFC3339): " + err.Error()})
			return
		}
		since = &t
		filter.Since = &t
	}

	if v := ctx.Query("is_group"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid is_group: " + err.Error()})
			return
		}
		filter.IsGroup = &b
	}

	messagesPerChat := parseIntDefault(ctx.Query("messages_per_chat"), 200)

	page, err := h.service.GetImportPage(filter, since, messagesPerChat)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{
		"message":     "success",
		"data":        page.Items,
		"count":       len(page.Items),
		"next_cursor": page.NextCursor,
		"has_more":    page.HasMore,
	})
}

func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return def
	}
	return v
}
