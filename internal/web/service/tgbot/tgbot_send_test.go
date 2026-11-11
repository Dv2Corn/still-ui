package tgbot

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"

	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

func TestIsTelegramNotModifiedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not modified", errors.New("Bad Request: message is not modified"), true},
		{"No fields to modify", errors.New("Bad Request: No fields to modify"), true},
		{"unrelated error", errors.New("Bad Request: message to edit not found"), false},
		{"network error", errors.New("connection reset"), false},
		{"empty string", errors.New(""), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTelegramNotModifiedError(tt.err)
			if got != tt.want {
				t.Errorf("isTelegramNotModifiedError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// telego.NewBot rejects any token that does not match `^\d+:[\w-]{35}$`.
var testBotToken = "123456:" + strings.Repeat("a", 35)

// warningsMentioning returns the buffered warnings (or worse) that carry marker. The log is shared by
// every goroutine of the process, so a test finds its own text in it and never counts entries.
func warningsMentioning(marker string) []string {
	var found []string
	for _, entry := range logger.GetLogs(math.MaxInt, "warning") {
		if strings.Contains(entry, marker) {
			found = append(found, entry)
		}
	}
	return found
}

// notModifiedBot answers every call with Telegram's "not modified" error; the description carries
// the test's name, so a warning about that error is recognisable as this test's own.
func notModifiedBot(t *testing.T) {
	t.Helper()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  400,
			"description": "Bad Request: message is not modified: " + t.Name(),
		})
	}))
	t.Cleanup(mock.Close)
	swapTestBot(t, mock.URL)
}

func TestEditMessageTgBotSkipsNotModified(t *testing.T) {
	notModifiedBot(t)

	tb := &Tgbot{}
	tb.editMessageTgBot(123, 456, "<b>hello</b>")

	if logged := warningsMentioning(t.Name()); len(logged) != 0 {
		t.Errorf("editMessageTgBot warned about an unchanged message: %v", logged)
	}
}

func TestEditMessageCallbackTgBotSkipsNotModified(t *testing.T) {
	notModifiedBot(t)

	tb := &Tgbot{}
	kb := tu.InlineKeyboard(tu.InlineKeyboardRow(
		tu.InlineKeyboardButton("btn").WithCallbackData("test"),
	))
	tb.editMessageCallbackTgBot(123, 456, kb)

	if logged := warningsMentioning(t.Name()); len(logged) != 0 {
		t.Errorf("editMessageCallbackTgBot warned about an unchanged reply markup: %v", logged)
	}
}

func TestPageMessageSplitsLinkListWithoutBlankLines(t *testing.T) {
	var message strings.Builder
	message.WriteString("Individual links:\r\n")
	for range 50 {
		message.WriteString("<code>vless://" + strings.Repeat("a", 300) + "</code>\r\n")
	}

	pages := pageMessage(message.String(), telegramPageLimit)
	if len(pages) < 2 {
		t.Fatalf("pageMessage() returned %d page, want multiple", len(pages))
	}

	links := 0
	for index, page := range pages {
		if len(page) > telegramPageLimit {
			t.Errorf("page %d has %d bytes, want at most %d", index, len(page), telegramPageLimit)
		}
		openingTags := strings.Count(page, "<code>")
		closingTags := strings.Count(page, "</code>")
		if openingTags != closingTags {
			t.Errorf("page %d has %d opening tags and %d closing tags", index, openingTags, closingTags)
		}
		links += openingTags
	}
	if links != 50 {
		t.Errorf("pages contain %d links, want 50", links)
	}
}

// Regression test: callback answers are shown as plain text, so the entities
// I18nBot escapes for HTML-parsed messages must not surface in them.
func TestCallbackAnswerIsPlainText(t *testing.T) {
	var mu sync.Mutex
	var texts []string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testBotToken+"/answerCallbackQuery" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		texts = append(texts, body.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer mock.Close()
	swapTestBot(t, mock.URL)
	draftLocalizer(t, &i18n.Message{ID: "tgbot.answers.canceled", Other: "❌ {{ .Email }}: Operation canceled."})

	tb := &Tgbot{}
	tb.sendCallbackAnswerTgBot("q1", tb.I18nBot("tgbot.answers.canceled", "Email==a&b<c>"))

	mu.Lock()
	defer mu.Unlock()
	if len(texts) != 1 || texts[0] != "❌ a&b<c>: Operation canceled." {
		t.Errorf("answerCallbackQuery texts = %q, want the e-mail shown verbatim", texts)
	}
}
