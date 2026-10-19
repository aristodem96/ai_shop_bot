package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Categories is the category tree the owner edits from the managers' chat.
// categories.Service is the implementation.
type Categories interface {
	Tree(ctx context.Context) ([]categories.Category, error)
	Word(ctx context.Context, word string) (*categories.WordInfo, error)
	Plan(ctx context.Context, op categories.Op) (*categories.Plan, error)
	Apply(ctx context.Context, op categories.Op) (*categories.Plan, error)
}

// Commands of the managers' chat for the tree. Latin: Telegram treats only
// Latin as a command, and in a group in privacy mode delivers nothing else
// unasked.
const (
	cmdCategories = "/categories"
	cmdWord       = "/word"
)

// catTTL is how long a started change waits for its reply or button.
const catTTL = 30 * time.Minute

// catJob is a change the owner has started: waiting for a typed title or
// word, or for Apply under its plan.
type catJob struct {
	id      int64
	chat    int64
	op      categories.Op
	created time.Time
}

func (j *catJob) jobID() int64             { return j.id }
func (j *catJob) setJobID(id int64)        { j.id = id }
func (j *catJob) createdAt() time.Time     { return j.created }
func (j *catJob) setCreatedAt(t time.Time) { j.created = t }

type catJobJSON struct {
	ID      int64         `json:"id"`
	Chat    int64         `json:"chat"`
	Op      categories.Op `json:"op"`
	Created time.Time     `json:"created"`
}

// MarshalJSON writes the catJob for Redis.
func (j *catJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(catJobJSON{ID: j.id, Chat: j.chat, Op: j.op, Created: j.created})
}

// UnmarshalJSON reads the catJob back.
func (j *catJob) UnmarshalJSON(b []byte) error {
	var x catJobJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*j = catJob{id: x.ID, chat: x.Chat, op: x.Op, created: x.Created}
	return nil
}

// UseCategories turns on /categories and /word in the managers' chat.
func (b *Bot) UseCategories(c Categories) {
	b.cats = c
	b.catJobs = newJobs[catJob](catTTL, b.dialogs("dialog:cat:", catTTL), b.dialogs("dialog:cat-reply:", catTTL), b.log)
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

// handleCategoriesMessage takes /categories, /word and the replies a change
// waits for. It reports whether the message was one of them.
func (b *Bot) handleCategoriesMessage(ctx context.Context, m *telegram.Message) bool {
	chat := m.Chat.ID
	if _, ok := commandArg(m.Text, cmdCategories); ok {
		b.catJobs.forget(ctx, chat)
		text, kb, err := b.categoryScreen(ctx, 0, "")
		if err != nil {
			b.fail(ctx, chat, "show categories", err)
			return true
		}
		b.send(ctx, chat, text, kb)
		return true
	}
	if arg, ok := commandArg(m.Text, cmdWord); ok {
		b.catJobs.forget(ctx, chat)
		b.startWord(ctx, chat, arg)
		return true
	}
	if m.Text == "" || strings.HasPrefix(m.Text, "/") {
		return false
	}
	job, ok := b.catJobs.awaited(ctx, chat)
	if !ok {
		return false
	}
	switch job.op.Kind {
	case categories.AddWord:
		job.op.Word = m.Text
	default:
		job.op.Title = m.Text
	}
	b.catJobs.save(ctx, job)
	b.planChange(ctx, callbackCtx{chat: chat}, job)
	return true
}

// startWord is "/word вал распредел": where the word's products are now,
// and the tree to pick its category from.
func (b *Bot) startWord(ctx context.Context, chat int64, arg string) {
	if arg == "" {
		b.send(ctx, chat, msgWordHelp, nil)
		return
	}
	info, err := b.cats.Word(ctx, arg)
	if err != nil {
		b.ownerError(ctx, callbackCtx{chat: chat}, err)
		return
	}
	if info.Owner != "" {
		b.send(ctx, chat, wordTakenText(info), nil)
		return
	}
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return
	}
	job := b.catJobs.add(ctx, &catJob{chat: chat, op: categories.Op{Kind: categories.AddWord, Word: info.Word}})
	b.send(ctx, chat, wordStartText(info), pickKeyboard(job.id, tree, 0))
}

// routeCategories dispatches the tree's buttons.
func (b *Bot) routeCategories(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	a := cb.args
	switch cb.act {
	case actCatOpen:
		return "", b.showCategory(ctx, cc, a[0], "")
	case actCatNew:
		return "", b.askCategory(ctx, cc, categories.Op{Kind: categories.Create, CategoryID: a[0]}, a[0])
	case actCatRename:
		return "", b.askCategory(ctx, cc, categories.Op{Kind: categories.Rename, CategoryID: a[0]}, a[0])
	case actCatAddWord:
		return "", b.askCategory(ctx, cc, categories.Op{Kind: categories.AddWord, CategoryID: a[0]}, a[0])
	case actCatRemoveWord:
		job := b.catJobs.add(ctx, &catJob{chat: cc.chat, op: categories.Op{Kind: categories.RemoveWord, WordID: a[0]}})
		b.planChange(ctx, cc, job)
		return "", nil
	case actCatDelete:
		job := b.catJobs.add(ctx, &catJob{chat: cc.chat, op: categories.Op{Kind: categories.Delete, CategoryID: a[0]}})
		b.planChange(ctx, cc, job)
		return "", nil
	case actCatPick:
		return "", b.pickForWord(ctx, cc, a[0], a[1])
	case actCatApply:
		return b.applyChange(ctx, cc, a[0])
	default: // actCatCancel
		job, ok := b.catJobs.take(ctx, a[0])
		b.catJobs.forget(ctx, cc.chat)
		back := int64(0)
		if ok {
			back = job.op.CategoryID
		}
		return msgCatCancelled, b.showCategory(ctx, cc, back, "")
	}
}

// askCategory asks for a typed title or word, by reply: in a group in
// privacy mode the bot sees only replies to itself.
func (b *Bot) askCategory(ctx context.Context, cc callbackCtx, op categories.Op, id int64) error {
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		return err
	}
	var question string
	switch op.Kind {
	case categories.Create:
		question = msgCatNewSection
		if c, ok := findCategory(tree, id); ok {
			question = fmt.Sprintf(msgCatNewSub, esc(c.Title))
		}
	case categories.Rename:
		c, ok := findCategory(tree, id)
		if !ok {
			return b.showCategory(ctx, cc, 0, msgCatGone)
		}
		question = fmt.Sprintf(msgCatRename, esc(c.Title))
	default:
		c, ok := findCategory(tree, id)
		if !ok {
			return b.showCategory(ctx, cc, 0, msgCatGone)
		}
		question = fmt.Sprintf(msgCatAskWord, esc(string(pathOf(tree, c))))
	}
	job := b.catJobs.add(ctx, &catJob{chat: cc.chat, op: op})
	b.catJobs.await(ctx, cc.chat, job.id)
	return b.tg.SendMessage(ctx, cc.chat, question, &telegram.ForceReply{ForceReply: true})
}

// pickForWord is a category button under /word: a section with
// subcategories opens them, anything else is the word's category.
func (b *Bot) pickForWord(ctx context.Context, cc callbackCtx, id, categoryID int64) error {
	job, ok := b.catJobs.get(ctx, id)
	if !ok {
		return b.editText(ctx, cc, msgCatExpired, nil)
	}
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		return err
	}
	if categoryID != 0 && hasSubcategories(tree, categoryID) || categoryID == 0 {
		return b.editText(ctx, cc, fmt.Sprintf(msgWordPick, esc(job.op.Word)), pickKeyboard(job.id, tree, categoryID))
	}
	job.op.CategoryID = categoryID
	b.catJobs.save(ctx, job)
	b.planChange(ctx, cc, job)
	return nil
}

// planChange shows what a change would do, with Apply under it. A rename
// moves nothing and asks nothing, nor does a new category with nothing to
// move into it: they are applied at once.
func (b *Bot) planChange(ctx context.Context, cc callbackCtx, job *catJob) {
	p, err := b.cats.Plan(ctx, job.op)
	if err != nil {
		b.catJobs.remove(ctx, job.id)
		b.ownerError(ctx, cc, err)
		return
	}
	if p.Op.Kind == categories.Rename || (p.Op.Kind == categories.Create && len(p.Moves) == 0) {
		if _, err := b.applyChange(ctx, cc, job.id); err != nil {
			b.ownerError(ctx, cc, err)
		}
		return
	}
	job.op = p.Op
	b.catJobs.save(ctx, job)
	text, kb := planView(job.id, p)
	b.show(ctx, cc, text, kb)
}

// applyChange writes a change, planned again first, and shows the category
// it touched.
func (b *Bot) applyChange(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	job, ok := b.catJobs.take(ctx, id)
	if !ok {
		return msgCatExpired, b.removeButtons(ctx, cc)
	}
	p, err := b.cats.Apply(ctx, job.op)
	if err != nil {
		b.ownerError(ctx, cc, err)
		return "", nil
	}
	back := p.Op.CategoryID
	switch p.Op.Kind {
	case categories.Delete:
		back = p.ParentID
	case categories.Create:
		// The new category, ready for its first word.
		if tree, err := b.cats.Tree(ctx); err == nil {
			for _, c := range tree {
				if c.ParentID == p.ParentID && c.Title == p.Op.Title {
					back = c.ID
				}
			}
		}
	}
	return msgCatDone, b.showCategory(ctx, cc, back, doneText(p))
}

// showCategory draws a category's screen -- the root for id zero -- in the
// pressed message, or a new one.
func (b *Bot) showCategory(ctx context.Context, cc callbackCtx, id int64, note string) error {
	text, kb, err := b.categoryScreen(ctx, id, note)
	if err != nil {
		return err
	}
	b.show(ctx, cc, text, kb)
	return nil
}

func (b *Bot) categoryScreen(ctx context.Context, id int64, note string) (string, *telegram.InlineKeyboard, error) {
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		return "", nil, err
	}
	c, ok := findCategory(tree, id)
	if id == 0 || !ok {
		text, kb := rootView(tree)
		if id != 0 {
			note = msgCatGone
		}
		return withNote(note, text), kb, nil
	}
	text, kb := categoryView(tree, c)
	return withNote(note, text), kb, nil
}

// show edits the pressed message, or sends a new one after a typed reply.
func (b *Bot) show(ctx context.Context, cc callbackCtx, text string, kb *telegram.InlineKeyboard) {
	if err := b.editText(ctx, cc, text, kb); err != nil {
		b.send(ctx, cc.chat, text, kb)
	}
}

// ownerError shows a refusal worded for the owner, or logs a fault.
func (b *Bot) ownerError(ctx context.Context, cc callbackCtx, err error) {
	for _, e := range []error{categories.ErrBadWord, categories.ErrWordTaken, categories.ErrBadTitle,
		categories.ErrTitleTaken, categories.ErrNoCategory, categories.ErrHasChildren, categories.ErrKeepOther,
		categories.ErrTooDeep, categories.ErrNoWord, categories.ErrNotForWords} {
		if errors.Is(err, e) {
			b.send(ctx, cc.chat, "❌ "+esc(err.Error()), nil)
			return
		}
	}
	b.fail(ctx, cc.chat, "change categories", err)
}

func withNote(note, text string) string {
	if note == "" {
		return text
	}
	return note + "\n\n" + text
}

func findCategory(tree []categories.Category, id int64) (categories.Category, bool) {
	for _, c := range tree {
		if c.ID == id {
			return c, true
		}
	}
	return categories.Category{}, false
}

func hasSubcategories(tree []categories.Category, id int64) bool {
	for _, c := range tree {
		if c.ParentID == id {
			return true
		}
	}
	return false
}

func pathOf(tree []categories.Category, c categories.Category) categories.Path {
	if p, ok := findCategory(tree, c.ParentID); ok && c.ParentID != 0 {
		return categories.Path(p.Title + " › " + c.Title)
	}
	return categories.Path(c.Title)
}
