package chathistory_extractor

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
)

type Extracted struct {
	MessageType string
	Content     string
	MediaURL    string
	MediaMime   string
	QuotedID    string
}

// Extract pulls a normalized representation out of the whatsmeow waE2E.Message oneof.
// Returns ("", "", ...) for unsupported / control messages so callers can skip persistence.
func Extract(msg *waE2E.Message) Extracted {
	if msg == nil {
		return Extracted{}
	}

	if c := msg.GetConversation(); c != "" {
		return Extracted{MessageType: "text", Content: c}
	}

	if ext := msg.GetExtendedTextMessage(); ext != nil {
		out := Extracted{MessageType: "text", Content: ext.GetText()}
		if ctx := ext.GetContextInfo(); ctx != nil {
			out.QuotedID = ctx.GetStanzaID()
		}
		return out
	}

	if img := msg.GetImageMessage(); img != nil {
		return Extracted{
			MessageType: "image",
			Content:     img.GetCaption(),
			MediaURL:    img.GetURL(),
			MediaMime:   img.GetMimetype(),
			QuotedID:    img.GetContextInfo().GetStanzaID(),
		}
	}

	if vid := msg.GetVideoMessage(); vid != nil {
		return Extracted{
			MessageType: "video",
			Content:     vid.GetCaption(),
			MediaURL:    vid.GetURL(),
			MediaMime:   vid.GetMimetype(),
			QuotedID:    vid.GetContextInfo().GetStanzaID(),
		}
	}

	if aud := msg.GetAudioMessage(); aud != nil {
		return Extracted{
			MessageType: "audio",
			MediaURL:    aud.GetURL(),
			MediaMime:   aud.GetMimetype(),
			QuotedID:    aud.GetContextInfo().GetStanzaID(),
		}
	}

	if doc := msg.GetDocumentMessage(); doc != nil {
		return Extracted{
			MessageType: "document",
			Content:     doc.GetCaption(),
			MediaURL:    doc.GetURL(),
			MediaMime:   doc.GetMimetype(),
			QuotedID:    doc.GetContextInfo().GetStanzaID(),
		}
	}

	if st := msg.GetStickerMessage(); st != nil {
		return Extracted{
			MessageType: "sticker",
			MediaURL:    st.GetURL(),
			MediaMime:   st.GetMimetype(),
			QuotedID:    st.GetContextInfo().GetStanzaID(),
		}
	}

	if loc := msg.GetLocationMessage(); loc != nil {
		return Extracted{MessageType: "location", Content: loc.GetComment()}
	}

	if ct := msg.GetContactMessage(); ct != nil {
		return Extracted{MessageType: "contact", Content: ct.GetDisplayName()}
	}

	if rx := msg.GetReactionMessage(); rx != nil {
		return Extracted{
			MessageType: "reaction",
			Content:     rx.GetText(),
			QuotedID:    rx.GetKey().GetID(),
		}
	}

	return Extracted{}
}
