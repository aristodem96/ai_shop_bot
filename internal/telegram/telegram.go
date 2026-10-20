// Package telegram is a minimal Telegram Bot API client: long polling, sending
// and editing messages, inline keyboards and callback answers. A handful of
// methods do not justify a dependency, and a hand-written client is trivially
// faked in tests.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// ErrNotModified is returned when an edit would leave a message as it was.
// Telegram treats that as an error; for a bot that re-renders a cart after
// every tap it is the normal outcome of a double tap, so callers ignore it.
var ErrNotModified = errors.New("telegram: message is not modified")

// Client talks to one bot.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for the bot with the given token.
func New(token string) *Client {
	return NewWithBaseURL(defaultBaseURL, token)
}

// NewWithBaseURL points the client somewhere else; tests use it.
func NewWithBaseURL(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// Must outlast the long-poll timeout, or every idle poll would be
		// reported as a failure.
		http: &http.Client{Timeout: PollTimeout + 30*time.Second},
	}
}

// User is a Telegram account.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is where a message was sent.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// ChatPrivate is a one-to-one conversation with the bot.
const ChatPrivate = "private"

// Message is a message. For a callback on a message too old to be read, only
// Chat and MessageID are filled, which is all an edit needs.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	// Photo is the same picture in several sizes, smallest first.
	Photo []PhotoSize `json:"photo"`
	// Caption is the text sent with a photo.
	Caption string `json:"caption"`
	// Contact is a phone number shared with the "share contact" button.
	Contact *Contact `json:"contact"`
	// Document is a file sent as a file, not as a photo.
	Document *Document `json:"document"`
	// ReplyToMessage is the message this one answers, as a ForceReply
	// question gets answered.
	ReplyToMessage *Message `json:"reply_to_message"`
}

// Document is a file attached to a message.
type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	// FileSize is in bytes; Telegram may omit it.
	FileSize int64 `json:"file_size"`
}

// Contact is a shared phone number. UserID is set when the contact is a
// Telegram user, which tells the buyer's own number from someone else's.
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	UserID      int64  `json:"user_id"`
}

// PhotoSize is one size of a photo.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// CallbackQuery is a press of an inline button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one event from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// ChatID returns the chat an update belongs to, or 0 if it has none.
func (u Update) ChatID() int64 {
	switch {
	case u.Message != nil:
		return u.Message.Chat.ID
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		return u.CallbackQuery.Message.Chat.ID
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID
	}
	return 0
}

// ReplyMarkup is a keyboard attached to a message. The unexported method
// limits it to the two kinds below, so a typo cannot send arbitrary JSON.
type ReplyMarkup interface{ replyMarkup() }

// InlineKeyboard is a grid of buttons under a message.
type InlineKeyboard struct {
	Rows [][]InlineButton `json:"inline_keyboard"`
}

func (*InlineKeyboard) replyMarkup() {}

// MarshalJSON writes no rows as [] rather than null: Telegram rejects the whole
// message over a null keyboard, and a nil slice is the natural zero value here.
func (k *InlineKeyboard) MarshalJSON() ([]byte, error) {
	rows := k.Rows
	if rows == nil {
		rows = [][]InlineButton{}
	}
	return json.Marshal(struct {
		Rows [][]InlineButton `json:"inline_keyboard"`
	}{rows})
}

// InlineButton sends CallbackData back to the bot when pressed, or opens URL.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// ReplyKeyboard replaces the phone keyboard with buttons that send their text.
type ReplyKeyboard struct {
	Rows [][]KeyboardButton `json:"keyboard"`
	// Resize fits the keyboard to its buttons instead of half the screen.
	Resize bool `json:"resize_keyboard,omitempty"`
	// Persistent keeps it shown even after the user hides it.
	Persistent bool `json:"is_persistent,omitempty"`
	// Placeholder is the hint in the empty input field, up to 64 characters.
	Placeholder string `json:"input_field_placeholder,omitempty"`
}

func (*ReplyKeyboard) replyMarkup() {}

// ForceReply opens a reply to the message in the user's app. In a group a bot
// in privacy mode sees only commands and replies to itself, so a question
// whose answer is plain text has to be asked this way.
type ForceReply struct {
	ForceReply  bool   `json:"force_reply"`
	Placeholder string `json:"input_field_placeholder,omitempty"`
}

func (*ForceReply) replyMarkup() {}

// KeyboardButton is a reply keyboard button. With RequestContact set, a press
// shares the user's phone number instead of sending the text.
type KeyboardButton struct {
	Text           string `json:"text"`
	RequestContact bool   `json:"request_contact,omitempty"`
}

// PollTimeout is how long a single getUpdates call waits for new events.
// Long polling is what lets the bot run without a public URL of its own.
const PollTimeout = 30 * time.Second

// GetUpdates long-polls for events after offset, waiting up to timeout.
//
// Passing an offset is also what acknowledges everything before it: Telegram
// forgets an update only once a later call asks for a higher offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

// SendMessage sends HTML-formatted text.
func (c *Client) SendMessage(ctx context.Context, chatID int64, html string, markup ReplyMarkup) error {
	params := map[string]any{
		"chat_id":              chatID,
		"text":                 html,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if present(markup) {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "sendMessage", params, nil)
}

// SendPhoto sends a photo by URL with an HTML caption. Telegram downloads the
// photo itself, so the bot never holds the bytes.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, photoURL, captionHTML string, markup ReplyMarkup) error {
	params := map[string]any{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    captionHTML,
		"parse_mode": "HTML",
	}
	if present(markup) {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "sendPhoto", params, nil)
}

// SendDocument sends a file, such as a spreadsheet, with a caption.
func (c *Client) SendDocument(ctx context.Context, chatID int64, filename string, data []byte, captionHTML string) error {
	body, contentType, err := multipartBody(map[string]string{
		"chat_id": strconv.FormatInt(chatID, 10), "caption": captionHTML, "parse_mode": "HTML",
	}, "document", filename, data)
	if err != nil {
		return err
	}
	return c.send(ctx, "sendDocument", body, contentType, nil)
}

// multipartBody is a form with fields and one file.
func multipartBody(fields map[string]string, fileField, filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", fmt.Errorf("telegram: write %s: %w", k, err)
		}
	}
	fw, err := w.CreateFormFile(fileField, filename)
	if err != nil {
		return nil, "", fmt.Errorf("telegram: attach %s: %w", fileField, err)
	}
	if _, err := fw.Write(data); err != nil {
		return nil, "", fmt.Errorf("telegram: attach %s: %w", fileField, err)
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("telegram: close upload: %w", err)
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// UploadPhoto sends a photo from its bytes and returns the file_id Telegram
// keeps it under: sent again by that id, as SendPhoto's photo, it is not
// uploaded twice.
func (c *Client) UploadPhoto(ctx context.Context, chatID int64, filename string, data []byte, captionHTML string, markup ReplyMarkup) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fields := [][2]string{{"chat_id", strconv.FormatInt(chatID, 10)}, {"caption", captionHTML}, {"parse_mode", "HTML"}}
	if present(markup) {
		raw, err := json.Marshal(markup)
		if err != nil {
			return "", fmt.Errorf("telegram: encode reply markup: %w", err)
		}
		fields = append(fields, [2]string{"reply_markup", string(raw)})
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return "", fmt.Errorf("telegram: write %s: %w", f[0], err)
		}
	}
	fw, err := w.CreateFormFile("photo", filename)
	if err != nil {
		return "", fmt.Errorf("telegram: attach photo: %w", err)
	}
	if _, err := fw.Write(data); err != nil {
		return "", fmt.Errorf("telegram: attach photo: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("telegram: close upload: %w", err)
	}
	var sent Message
	if err := c.send(ctx, "sendPhoto", buf.Bytes(), w.FormDataContentType(), &sent); err != nil {
		return "", err
	}
	// Telegram keeps the photo in several sizes; any id sends all of them.
	if len(sent.Photo) == 0 {
		return "", errors.New("telegram: sendPhoto returned no photo")
	}
	return sent.Photo[len(sent.Photo)-1].FileID, nil
}

// present reports whether a keyboard was given. A nil *InlineKeyboard in a
// ReplyMarkup is an interface that is not nil, and it marshals to null,
// which Telegram rejects with the whole message ("object expected as reply
// markup"). A caller passing "no keyboard" that way gets no keyboard.
func present(m ReplyMarkup) bool {
	switch v := m.(type) {
	case nil:
		return false
	case *InlineKeyboard:
		return v != nil
	case *ReplyKeyboard:
		return v != nil
	case *ForceReply:
		return v != nil
	}
	return true
}

// EditMessageText replaces the text and keyboard of a text message.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, html string, markup *InlineKeyboard) error {
	params := map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 html,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if markup != nil {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "editMessageText", params, nil)
}

// EditMessageReplyMarkup replaces only the keyboard, which works for photo
// messages as well as text ones.
func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *InlineKeyboard) error {
	return c.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": markup,
	}, nil)
}

// AnswerCallbackQuery stops the button's loading spinner, optionally with a
// short toast, or a dialog when alert is set. Every callback must be answered,
// or the button keeps spinning on the user's screen.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
		"show_alert":        alert,
	}, nil)
}

// GetMe returns the bot's own account.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, "getMe", map[string]any{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ChatActionTyping shows "typing..." in the chat header.
const ChatActionTyping = "typing"

// SendChatAction shows a status such as "typing..." for about five seconds or
// until the bot's next message, whichever comes first.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// File is a file stored on Telegram's servers, ready to download.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

// GetFile resolves a file id to a path DownloadFile can fetch. The path is
// valid for at least an hour.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	var f File
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// ErrFileTooLarge is returned by DownloadFile for a file over the limit.
var ErrFileTooLarge = errors.New("telegram: file too large")

// DownloadFile fetches a file by the path from GetFile, refusing anything
// larger than maxBytes. The limit is enforced while reading, not trusted from
// a header, so a lying server cannot make the bot buffer more than it allows.
func (c *Client) DownloadFile(ctx context.Context, filePath string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/file/bot"+c.token+"/"+filePath, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram: build download request: %w", redact(err))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: download file: %w", redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: download file: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("telegram: read file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrFileTooLarge
	}
	return data, nil
}

// Command is one entry of the menu Telegram shows under the message box.
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands registers the command menu.
func (c *Client) SetCommands(ctx context.Context, commands []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

// DeleteChatCommands takes down the command menu registered for one chat,
// which then shows the default menu again.
func (c *Client) DeleteChatCommands(ctx context.Context, chatID int64) error {
	return c.call(ctx, "deleteMyCommands", map[string]any{
		"scope": map[string]any{"type": "chat", "chat_id": chatID},
	}, nil)
}

// SetDescription sets what the bot's empty chat shows before Start (up to
// 512 characters) and the line on its profile (up to 120).
func (c *Client) SetDescription(ctx context.Context, description, short string) error {
	if err := c.call(ctx, "setMyDescription", map[string]any{"description": description}, nil); err != nil {
		return err
	}
	return c.call(ctx, "setMyShortDescription", map[string]any{"short_description": short}, nil)
}

// SetChatCommands registers the command menu of one chat, which replaces the
// default menu there: the managers' chat gets its own commands while buyers
// keep theirs.
func (c *Client) SetChatCommands(ctx context.Context, chatID int64, commands []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{
		"commands": commands,
		"scope":    map[string]any{"type": "chat", "chat_id": chatID},
	}, nil)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		// RetryAfter is how many seconds a flood-limited bot must wait.
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// maxRetryAfter is the longest flood wait the client sits out. Telegram asks
// for a few seconds when a chat gets messages too fast -- a manager tapping
// through a dialog -- and dropping the message then loses the answer to a
// tap. A longer wait is a real limit, and the caller hears of it.
const maxRetryAfter = 10 * time.Second

// floodRetries bounds the waits for one call.
const floodRetries = 2

func (c *Client) call(ctx context.Context, method string, params map[string]any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	return c.send(ctx, method, body, "application/json", out)
}

// send makes a call, sitting out a short flood wait.
func (c *Client) send(ctx context.Context, method string, body []byte, contentType string, out any) error {
	for attempt := 0; ; attempt++ {
		wait, err := c.do(ctx, method, body, contentType, out)
		if wait == 0 || attempt == floodRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}

// do makes one call. A short flood wait comes back as wait, with the error
// to return if the caller does not retry.
func (c *Client) do(ctx context.Context, method string, body []byte, contentType string, out any) (wait time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("telegram: build %s request: %w", method, redact(err))
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("telegram: %s: %w", method, redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, fmt.Errorf("telegram: read %s response: %w", method, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, fmt.Errorf("telegram: %s: unexpected response (HTTP %d)", method, resp.StatusCode)
	}
	if !env.OK {
		if strings.Contains(env.Description, "message is not modified") {
			return 0, ErrNotModified
		}
		err := fmt.Errorf("telegram: %s failed (%d): %s", method, env.ErrorCode, env.Description)
		retry := time.Duration(env.Parameters.RetryAfter) * time.Second
		if env.ErrorCode == http.StatusTooManyRequests && retry > 0 && retry <= maxRetryAfter {
			return retry, err
		}
		return 0, err
	}
	if out == nil {
		return 0, nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return 0, fmt.Errorf("telegram: decode %s result: %w", method, err)
	}
	return 0, nil
}

// redact strips the request URL from a transport error.
//
// The bot token is part of every Bot API URL, and net/http formats errors as
// `Post "https://api.telegram.org/bot<token>/getUpdates": dial tcp: ...`. Logged
// as is, the first network blip would write the token -- full control of the
// bot -- into the logs.
func redact(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
