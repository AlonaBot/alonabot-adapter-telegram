package telegram

import (
	"encoding/json"
	"fmt"

	msg "github.com/AlonaBot/AlonaBot/sdk/message"
)

const (
	albumRule   = "telegram.album"
	replyRule   = "telegram.reply_first"
	buttonsRule = "telegram.buttons_last"
)

func messageFormats() []msg.MessageFormat {
	formats := msg.StandardFormats()
	for index := range formats {
		format := &formats[index]
		format.Status = msg.SupportSupported
		switch format.Name {
		case "text", "markdown":
			format.TextLimit = &msg.TextLimit{Max: maxTextUTF16, Unit: msg.TextUTF16CodeUnits}
			format.Description += " Adjacent text and mentions share the text limit; explicit entities cannot be mixed with native MarkdownV2."
		case "image", "video", "voice", "file":
			format.Sources = []string{"https_url", "media_ref", "incoming", "inline_base64", "platform_file"}
			format.Description += fmt.Sprintf(" Caption limit: %d UTF-16 code units.", maxCaptionUTF16)
			if format.Name == "image" {
				format.Description += " Trusted image/gif references and GIF8 inline bytes are sent as standalone animations; HTTPS image URLs pass directly to Telegram."
			}
			if format.Name == "file" {
				format.Description += " Filename must not contain a path."
			}
		case "mention":
			format.Description += " User ID must be decimal."
		case "quote":
			format.Description += " At most one quote; it applies to the first physical delivery."
		case "forward":
			format.Status = msg.SupportUnsupported
			format.Description = "Telegram forward segments are unsupported."
		case "buttons":
			format.Description += " Labels are preserved in full and must be nonempty. At most one keyboard; it applies to the last physical delivery, which must be a single message."
		}
	}
	return formats
}

func combinationRules() []msg.CombinationRule {
	example := json.RawMessage(`{"parts":[{"type":"image","source":{"https_url":"https://example.com/first.jpg"}},{"type":"image","source":{"https_url":"https://example.com/second.jpg"}},{"type":"text","text":"Choose an option"},{"type":"buttons","rows":[[{"label":"Continue"}]]}]}`)
	return []msg.CombinationRule{
		{Name: albumRule, Formats: []string{"image", "video"}, Description: fmt.Sprintf("Consecutive images and videos form albums of at most %d items. A single item is sent separately. GIF animations and voice messages break albums.", maxAlbumMedia), ExampleJSON: example},
		{Name: replyRule, Formats: []string{"quote", "text", "markdown", "mention", "image", "video", "voice", "file"}, Description: "At most one quote; it applies only to the first physical delivery and must refer to the same chat.", ExampleJSON: json.RawMessage(`{"parts":[{"type":"quote","message_ref":"<confirmed-message-ref>"},{"type":"image","source":{"https_url":"https://example.com/first.jpg"}},{"type":"text","text":"Details"}]}`)},
		{Name: buttonsRule, Formats: []string{"buttons", "text", "markdown", "mention", "image", "video", "voice", "file"}, Description: "At most one keyboard; it attaches only to the last physical delivery. An album cannot carry buttons: add text after the album, then buttons.", ExampleJSON: example},
	}
}
