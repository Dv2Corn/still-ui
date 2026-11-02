package tgbot

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

const telegramPageLimit = 2000

func pageMessage(message string, limit int) []string {
	if len(message) <= limit {
		return []string{message}
	}

	pages := make([]string, 0)
	for block := range strings.SplitSeq(message, "\r\n\r\n") {
		for _, page := range splitMessageLines(block, limit) {
			last := len(pages) - 1
			if last >= 0 && len(pages[last])+len("\r\n\r\n")+len(page) <= limit {
				pages[last] += "\r\n\r\n" + page
				continue
			}
			pages = append(pages, page)
		}
	}
	if len(pages) > 0 && strings.TrimSpace(pages[len(pages)-1]) == "" {
		pages = pages[:len(pages)-1]
	}
	return pages
}

func splitMessageLines(block string, limit int) []string {
	if len(block) <= limit {
		return []string{block}
	}

	lines := strings.Split(block, "\r\n")
	pages := []string{}
	for _, line := range lines {
		// A single link can exceed a caption on its own; Telegram counts bytes,
		// so such a line is split rather than kept whole and rejected.
		for len(line) > limit {
			head, rest := cutHTMLLine(line, limit)
			pages = append(pages, head)
			line = rest
		}
		if len(pages) == 0 {
			pages = append(pages, line)
			continue
		}
		last := len(pages) - 1
		if len(pages[last])+len("\r\n")+len(line) > limit {
			pages = append(pages, line)
			continue
		}
		pages[last] += "\r\n" + line
	}
	if len(pages) == 0 {
		pages = append(pages, "")
	}
	return pages
}

// cutHTMLLine splits off a head of at most limit bytes that Telegram's HTML mode
// still accepts on its own: the cut never falls inside a tag, an entity or a
// UTF-8 character, and tags still open there are closed at the end of the head
// and reopened at the start of rest. Falls back to a byte cut when no such
// point exists (a tag longer than the page).
func cutHTMLLine(line string, limit int) (head, rest string) {
	type openTag struct{ name, raw string }
	var open, bestOpen []openTag
	closers := func(tags []openTag) string {
		var b strings.Builder
		for i := len(tags) - 1; i >= 0; i-- {
			b.WriteString("</" + tags[i].name + ">")
		}
		return b.String()
	}
	best := -1
	for i := 0; i < len(line) && i <= limit; {
		if i > 0 && i+len(closers(open)) <= limit {
			best, bestOpen = i, slices.Clone(open)
		}
		switch line[i] {
		case '<':
			end := strings.IndexByte(line[i:], '>')
			if end < 0 {
				i++
				continue
			}
			tag := line[i : i+end+1]
			name := strings.TrimPrefix(strings.Trim(tag, "<>"), "/")
			if sp := strings.IndexAny(name, " \t"); sp >= 0 {
				name = name[:sp]
			}
			if strings.HasPrefix(tag, "</") {
				for k := len(open) - 1; k >= 0; k-- {
					if open[k].name == name {
						open = open[:k]
						break
					}
				}
			} else if name != "" {
				open = append(open, openTag{name: name, raw: tag})
			}
			i += end + 1
		case '&':
			if end := strings.IndexByte(line[i:], ';'); end > 0 && end <= 10 {
				i += end + 1
			} else {
				i++
			}
		default:
			_, size := utf8.DecodeRuneInString(line[i:])
			i += size
		}
	}
	var reopen strings.Builder
	for _, t := range bestOpen {
		reopen.WriteString(t.raw)
	}
	// The rest must shrink, or a reopened tag as long as the page loops forever.
	if best <= reopen.Len() {
		return line[:limit], line[limit:]
	}
	return line[:best] + closers(bestOpen), reopen.String() + line[best:]
}

// SendMsgToTgbot sends a message with optional reply markup and returns the id
// of the last one delivered, or 0; only the broadcast prompt needs it.
func (t *Tgbot) SendMsgToTgbot(chatId int64, msg string, replyMarkup ...telego.ReplyMarkup) int {
	if !t.IsRunning() {
		return 0
	}

	if msg == "" {
		logger.Info("[tgbot] message is empty!")
		return 0
	}
	lastID := 0

	allMessages := pageMessage(msg, telegramPageLimit)
	for n, message := range allMessages {
		params := telego.SendMessageParams{
			ChatID:          tu.ID(chatId),
			MessageThreadID: adminTopic(chatId),
			Text:            message,
			ParseMode:       "HTML",
		}
		// only add replyMarkup to last message
		if len(replyMarkup) > 0 && n == (len(allMessages)-1) {
			params.ReplyMarkup = replyMarkup[0]
		}

		// Retry logic with exponential backoff for connection errors
		maxRetries := 3
		for attempt := range maxRetries {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			sent, err := liveBot().SendMessage(ctx, &params)
			cancel()

			if err == nil {
				if sent != nil {
					lastID = sent.MessageID
				}
				break // Success
			}

			// Check if error is a connection error
			errStr := err.Error()
			isConnectionError := strings.Contains(errStr, "connection") ||
				strings.Contains(errStr, "timeout") ||
				strings.Contains(errStr, "closed")

			if isConnectionError && attempt < maxRetries-1 {
				// Exponential backoff: 1s, 2s, 4s
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				logger.Warningf("Connection error sending telegram message (attempt %d/%d), retrying in %v: %v",
					attempt+1, maxRetries, backoff, err)
				time.Sleep(backoff)
			} else {
				logger.Warning("Error sending telegram message:", err)
				break
			}
		}

		// Reduced delay to improve performance (only needed for rate limiting)
		if n < len(allMessages)-1 { // Only delay between messages, not after the last one
			time.Sleep(100 * time.Millisecond)
		}
	}
	return lastID
}

// SendMsgToTgbotAdmins sends a message to all admin Telegram chats.
func (t *Tgbot) SendMsgToTgbotAdmins(msg string, replyMarkup ...telego.ReplyMarkup) {
	admins := adminSnapshot()
	if len(replyMarkup) > 0 {
		for _, adminId := range admins {
			t.SendMsgToTgbot(adminId, msg, replyMarkup[0])
		}
	} else {
		for _, adminId := range admins {
			t.SendMsgToTgbot(adminId, msg)
		}
	}
}

// sendCallbackAnswerTgBot answers a callback query with a message.
func (t *Tgbot) sendCallbackAnswerTgBot(id string, message string) {
	// Callback answers are plain text, so the HTML entities I18nBot adds are undone.
	params := telego.AnswerCallbackQueryParams{
		CallbackQueryID: id,
		Text:            html.UnescapeString(message),
	}
	if err := liveBot().AnswerCallbackQuery(context.Background(), &params); err != nil {
		logger.Warning(err)
	}
}

// editMessageCallbackTgBot edits the reply markup of a message.
func (t *Tgbot) editMessageCallbackTgBot(chatId int64, messageID int, inlineKeyboard *telego.InlineKeyboardMarkup) {
	b := liveBot()
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	params := telego.EditMessageReplyMarkupParams{
		ChatID:      tu.ID(chatId),
		MessageID:   messageID,
		ReplyMarkup: inlineKeyboard,
	}
	if _, err := b.EditMessageReplyMarkup(ctx, &params); err != nil {
		if isTelegramNotModifiedError(err) {
			logger.Debug("Telegram reply markup unchanged, skipping edit")
			return
		}
		logger.Warning(err)
	}
}

// editMessageTgBot edits the text and reply markup of a message.
func (t *Tgbot) editMessageTgBot(chatId int64, messageID int, text string, inlineKeyboard ...*telego.InlineKeyboardMarkup) {
	params := telego.EditMessageTextParams{
		ChatID:    tu.ID(chatId),
		MessageID: messageID,
		Text:      text,
		ParseMode: "HTML",
	}
	if len(inlineKeyboard) > 0 {
		params.ReplyMarkup = inlineKeyboard[0]
	}
	if _, err := liveBot().EditMessageText(context.Background(), &params); err != nil {
		if isTelegramNotModifiedError(err) {
			logger.Debug("Telegram message text unchanged, skipping edit")
			return
		}
		logger.Warning(err)
	}
}

// Telegram answers a no-op edit with a 400 whose description carries this text;
// a refresh tap that changed nothing is not an operator-visible failure.
func isTelegramNotModifiedError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "not modified") ||
		strings.Contains(errStr, "No fields to modify")
}

// sendPagedNotice posts one notice per page, each dismissable. Telegram rejects
// a message over 4096 characters whole, so a per-client report has to be split.
func (t *Tgbot) sendPagedNotice(chatID int64, text string) {
	for _, chunk := range pageMessage(text, telegramPageLimit) {
		t.sendNoticeReturningID(chatID, chunk)
	}
}

// deleteMessageTgBot deletes a message from the chat.
func (t *Tgbot) deleteMessageTgBot(chatId int64, messageID int) {
	if liveBot() == nil {
		return
	}
	params := telego.DeleteMessageParams{
		ChatID:    tu.ID(chatId),
		MessageID: messageID,
	}
	if err := liveBot().DeleteMessage(context.Background(), &params); err != nil {
		logger.Warning("Failed to delete message:", err)
	} else {
		logger.Info("Message deleted successfully")
	}
}

// TestConnection verifies the bot token is valid and the API is reachable.
func (t *Tgbot) TestConnection() error {
	b := liveBot()
	if b == nil {
		return fmt.Errorf("bot not initialized")
	}
	me, err := b.GetMe(context.Background())
	if err != nil {
		return fmt.Errorf("API unreachable: %w", err)
	}
	_ = me
	return nil
}
