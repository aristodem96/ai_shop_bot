package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Words lets the shop's owner teach the catalogue new words from the
// managers' chat: "/word вал распредел", a category picked by buttons, a
// preview of what moves, and Apply.
type Words interface {
	Categories(ctx context.Context) ([]importer.Category, error)
	KeywordMatches(ctx context.Context, word string) (total, inDefault int, err error)
	PreviewKeyword(ctx context.Context, word, slug string, create *importer.NewCategory) (*importer.KeywordChange, error)
	ApplyKeyword(ctx context.Context, word, slug string, create *importer.NewCategory) (*importer.KeywordChange, error)
	OwnerKeywords(ctx context.Context) ([]importer.OwnerKeyword, error)
	RemoveKeyword(ctx context.Context, id int64) (*importer.OwnerKeyword, error)
}

// Commands of the managers' chat. Latin: Telegram treats only Latin as a
// command, and in a group in privacy mode delivers nothing else unasked.
const (
	cmdWord  = "/word"
	cmdWords = "/words"
)

// wordTTL is how long a started change waits for its buttons.
const wordTTL = 30 * time.Minute

// wordJob is one "/word" dialog: the word and, once chosen, where it goes.
type wordJob struct {
	id      int64
	chat    int64
	word    string
	slug    string                // an existing category
	create  *importer.NewCategory // or a new one
	created time.Time
}

type wordJobs struct {
	mu   sync.Mutex
	next int64
	jobs map[int64]*wordJob
	// awaiting is the job whose new category's title the chat is typing.
	awaiting map[int64]int64
}

// UseWords turns on /word and /words in the managers' chat.
func (b *Bot) UseWords(words Words) {
	b.words = words
	b.wordJobs = &wordJobs{jobs: make(map[int64]*wordJob), awaiting: make(map[int64]int64)}
}

// commandArg matches a command, with or without the bot's name a group
// adds ("/word@shop_bot"), and returns what follows it.
func commandArg(text, cmd string) (string, bool) {
	head, rest, _ := strings.Cut(text, " ")
	head, _, _ = strings.Cut(head, "@")
	if head != cmd {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// handleWordsMessage takes the managers' chat's word commands and the title
// of a new category. It reports whether the message was one of them.
func (b *Bot) handleWordsMessage(ctx context.Context, m *telegram.Message) bool {
	chat := m.Chat.ID
	if arg, ok := commandArg(m.Text, cmdWord); ok {
		b.wordJobs.forget(chat)
		b.startWord(ctx, chat, arg)
		return true
	}
	if _, ok := commandArg(m.Text, cmdWords); ok {
		b.wordJobs.forget(chat)
		text, kb := b.wordsList(ctx)
		b.send(ctx, chat, text, kb)
		return true
	}
	if m.Text == "" || strings.HasPrefix(m.Text, "/") {
		return false
	}
	job, ok := b.wordJobs.awaitingTitle(chat)
	if !ok {
		return false
	}
	job.create.Title = m.Text
	b.previewWord(ctx, chat, job, 0)
	return true
}

func (b *Bot) startWord(ctx context.Context, chat int64, arg string) {
	word, err := importer.NormalizeKeyword(arg)
	if arg == "" {
		b.send(ctx, chat, msgWordHelp, nil)
		return
	}
	if err != nil {
		b.send(ctx, chat, "❌ "+esc(err.Error()), nil)
		return
	}
	total, inDefault, err := b.words.KeywordMatches(ctx, word)
	if err != nil {
		b.fail(ctx, chat, "count keyword matches", err)
		return
	}
	cats, err := b.words.Categories(ctx)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return
	}
	job := b.wordJobs.add(&wordJob{chat: chat, word: word, created: time.Now()})
	b.send(ctx, chat, wordStartText(word, total, inDefault), sectionsKeyboard(job.id, cats))
}

// pickWordCategory is a category button. A section with subcategories
// opens them; anything else is the target.
func (b *Bot) pickWordCategory(ctx context.Context, cc callbackCtx, id, idx int64) (string, error) {
	job, cats, err := b.wordStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	if idx < 0 || idx >= int64(len(cats)) {
		return "", errBadCallback
	}
	c := cats[idx]
	if hasChildren(cats, c.Slug) {
		return "", b.editText(ctx, cc, fmt.Sprintf(msgWordPickSub, esc(job.word), esc(c.Title)), subcategoriesKeyboard(job.id, cats, idx))
	}
	job.slug, job.create = c.Slug, nil
	b.previewWord(ctx, cc.chat, job, cc.msg)
	return "", nil
}

// newWordCategory asks for the title of a new category: a section when idx
// is -1, else a subcategory of the section at idx.
func (b *Bot) newWordCategory(ctx context.Context, cc callbackCtx, id, idx int64) (string, error) {
	job, cats, err := b.wordStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	question := msgWordNewSection
	create := &importer.NewCategory{}
	if idx >= 0 {
		if idx >= int64(len(cats)) || cats[idx].Parent != "" {
			return "", errBadCallback
		}
		create.Parent = cats[idx].Slug
		question = fmt.Sprintf(msgWordNewSub, esc(cats[idx].Title))
	}
	job.slug, job.create = "", create
	b.wordJobs.await(cc.chat, job.id)
	if err := b.removeButtons(ctx, cc); err != nil {
		return "", err
	}
	return "", b.tg.SendMessage(ctx, cc.chat, question, &telegram.ForceReply{ForceReply: true, Placeholder: "Назва"})
}

// backToSections returns the dialog to the list of sections.
func (b *Bot) backToSections(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	job, cats, err := b.wordStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	return "", b.editText(ctx, cc, fmt.Sprintf(msgWordPick, esc(job.word)), sectionsKeyboard(job.id, cats))
}

// previewWord shows what the change would move, editing the dialog's
// message when there is one.
func (b *Bot) previewWord(ctx context.Context, chat int64, job *wordJob, msg int64) {
	ch, err := b.words.PreviewKeyword(ctx, job.word, job.slug, job.create)
	if err != nil {
		if isOwnerError(err) {
			b.send(ctx, chat, "❌ "+esc(err.Error()), nil)
			return
		}
		b.fail(ctx, chat, "preview keyword", err)
		return
	}
	cats, err := b.words.Categories(ctx)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return
	}
	text, kb := wordPreviewView(job.id, ch, categoryPath(cats, ch.Target))
	if msg != 0 {
		if err := b.tg.EditMessageText(ctx, chat, msg, text, kb); err == nil {
			return
		}
	}
	b.send(ctx, chat, text, kb)
}

// applyWord stores the word and moves the products.
func (b *Bot) applyWord(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	job, ok := b.wordJobs.take(id)
	if !ok {
		return msgImportGone, b.removeButtons(ctx, cc)
	}
	ch, err := b.words.ApplyKeyword(ctx, job.word, job.slug, job.create)
	if err != nil {
		if isOwnerError(err) {
			return "", b.editText(ctx, cc, "❌ "+esc(err.Error()), nil)
		}
		return "", err
	}
	cats, err := b.words.Categories(ctx)
	if err != nil {
		return "", err
	}
	return "", b.editText(ctx, cc, fmt.Sprintf(msgWordApplied, esc(ch.Word), esc(categoryPath(cats, ch.Target)), num(len(ch.Move))), nil)
}

func (b *Bot) cancelWord(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	b.wordJobs.take(id)
	b.wordJobs.forget(cc.chat)
	return msgImportCancelled, b.editText(ctx, cc, msgWordCancelled, nil)
}

// deleteWord removes one of the owner's words from the /words list.
func (b *Bot) deleteWord(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	kw, err := b.words.RemoveKeyword(ctx, id)
	if errors.Is(err, importer.ErrBadKeyword) {
		return "Цього слова вже немає.", nil
	}
	if err != nil {
		return "", err
	}
	text, kb := b.wordsList(ctx)
	if err := b.editText(ctx, cc, text, kb); err != nil {
		return "", err
	}
	return fmt.Sprintf("Видалено «%s». Товари лишаються на місці до наступного імпорту.", kw.Word), nil
}

func (b *Bot) wordsList(ctx context.Context) (string, *telegram.InlineKeyboard) {
	kws, err := b.words.OwnerKeywords(ctx)
	if err != nil {
		b.log.ErrorContext(ctx, "list keywords failed", slog.Any("error", err))
		return msgInternalFail, nil
	}
	return wordsListView(kws)
}

// wordStep fetches a dialog for a button. A dialog gone stale -- applied,
// cancelled, expired -- removes the buttons and returns a nil job.
func (b *Bot) wordStep(ctx context.Context, cc callbackCtx, id int64) (*wordJob, []importer.Category, error) {
	if !b.isAdmin(cc.chat) {
		return nil, nil, errBadCallback
	}
	job, ok := b.wordJobs.get(id)
	if !ok {
		return nil, nil, b.editText(ctx, cc, msgWordExpired, nil)
	}
	cats, err := b.words.Categories(ctx)
	if err != nil {
		return nil, nil, err
	}
	return job, cats, nil
}

func isOwnerError(err error) bool {
	for _, e := range []error{importer.ErrBadKeyword, importer.ErrKeywordTaken, importer.ErrBadCategory, importer.ErrBadCategoryNm} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func hasChildren(cats []importer.Category, slug string) bool {
	for _, c := range cats {
		if c.Parent == slug {
			return true
		}
	}
	return false
}

// categoryPath names a category the way the menu shows it: "Двигун › Деталі
// двигуна".
func categoryPath(cats []importer.Category, c importer.Category) string {
	for _, p := range cats {
		if c.Parent != "" && p.Slug == c.Parent {
			return p.Title + " › " + c.Title
		}
	}
	return c.Title
}

func (s *wordJobs) add(job *wordJob) *wordJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, j := range s.jobs {
		if time.Since(j.created) > wordTTL {
			delete(s.jobs, id)
		}
	}
	s.next++
	job.id = s.next
	s.jobs[job.id] = job
	return job
}

func (s *wordJobs) get(id int64) (*wordJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok || time.Since(j.created) > wordTTL {
		return nil, false
	}
	return j, true
}

func (s *wordJobs) take(id int64) (*wordJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	delete(s.jobs, id)
	if !ok || time.Since(j.created) > wordTTL {
		return nil, false
	}
	return j, true
}

func (s *wordJobs) await(chat, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.awaiting[chat] = id
}

// awaitingTitle returns the job whose new category the chat names next,
// once: a second message is an ordinary message again.
func (s *wordJobs) awaitingTitle(chat int64) (*wordJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.awaiting[chat]
	if !ok {
		return nil, false
	}
	delete(s.awaiting, chat)
	j, ok := s.jobs[id]
	if !ok || j.create == nil || time.Since(j.created) > wordTTL {
		return nil, false
	}
	return j, true
}

func (s *wordJobs) forget(chat int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.awaiting, chat)
}
