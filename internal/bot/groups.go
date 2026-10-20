package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Grouping files a supplier's groups into the category tree, a model
// proposing. grouping.Service is the implementation.
type Grouping interface {
	CanSuggest() bool
	Sort(ctx context.Context, supplierID int64) (*grouping.Summary, error)
	Summary(ctx context.Context, supplierID int64) (*grouping.Summary, error)
	CreateProposed(ctx context.Context, supplierID int64) (created, filed int, err error)
	Doubts(ctx context.Context, supplierID int64) ([]grouping.Group, error)
	Decide(ctx context.Context, groupID, categoryID int64) (*grouping.Group, error)
	SortByName(ctx context.Context, groupID int64) (*grouping.Group, error)
	Get(ctx context.Context, id int64) (*grouping.Group, error)
	SuggestionPath(ctx context.Context, s *grouping.Suggestion) (string, error)
	Find(ctx context.Context, text string) ([]grouping.Group, error)
}

// Commands of the managers' chat for groups.
const (
	cmdGroups = "/groups"
	cmdGroup  = "/group"
)

// review is the owner going through a supplier's groups: after a file, or
// from /groups or /group. Its buttons carry only its id, so they fit
// Telegram's 64 bytes; where the review is lives here.
type review struct {
	id       int64
	chat     int64
	job      int64 // the file to check once done, zero for none
	supplier int64 // zero for a single group found by /group
	index    int   // the doubtful group shown
	created  time.Time
}

func (r *review) jobID() int64             { return r.id }
func (r *review) setJobID(id int64)        { r.id = id }
func (r *review) createdAt() time.Time     { return r.created }
func (r *review) setCreatedAt(t time.Time) { r.created = t }

type reviewJSON struct {
	ID       int64     `json:"id"`
	Chat     int64     `json:"chat"`
	Job      int64     `json:"job"`
	Supplier int64     `json:"supplier"`
	Index    int       `json:"index"`
	Created  time.Time `json:"created"`
}

// MarshalJSON writes the review for Redis.
func (r *review) MarshalJSON() ([]byte, error) {
	return json.Marshal(reviewJSON{ID: r.id, Chat: r.chat, Job: r.job, Supplier: r.supplier, Index: r.index, Created: r.created})
}

// UnmarshalJSON reads the review back.
func (r *review) UnmarshalJSON(b []byte) error {
	var x reviewJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*r = review{id: x.ID, chat: x.Chat, job: x.Job, supplier: x.Supplier, index: x.Index, created: x.Created}
	return nil
}

// reviewTTL is how long a review waits for its buttons.
const reviewTTL = 6 * time.Hour

// UseGrouping files suppliers' groups after a file is checked, and turns on
// /groups and /group. It needs UseImports and UseCategories.
func (b *Bot) UseGrouping(g Grouping) {
	b.grouping = g
	b.reviews = newJobs[review](reviewTTL, b.dialogs("dialog:review:", reviewTTL), nil, b.log)
}

// sortGroups runs after a file brought groups with no category: the model
// files what it is sure of, and what is left is put to the owner before
// the report.
func (b *Bot) sortGroups(ctx context.Context, job *importJob) {
	if !b.grouping.CanSuggest() {
		sum, err := b.grouping.Summary(ctx, job.supplier)
		b.groupsSorted(ctx, job, sum, err)
		return
	}
	// The model takes a minute a batch of eighty groups, and a 1C export
	// has hundreds: past the handler's deadline. It goes on in the
	// background, and what it filed is kept batch by batch, so a restart
	// halfway picks up where it stopped.
	b.send(ctx, job.chat, msgGroupsSorting, nil)
	b.later(ctx, func(ctx context.Context) {
		stop := b.keepTyping(ctx, job.chat)
		sum, err := b.grouping.Sort(ctx, job.supplier)
		stop()
		b.groupsSorted(ctx, job, sum, err)
	})
}

// groupsSorted shows what sorting a file's groups came to: the report, when
// nothing is left for the owner, or the proposals and doubts first.
func (b *Bot) groupsSorted(ctx context.Context, job *importJob, sum *grouping.Summary, err error) {
	if err != nil {
		b.log.WarnContext(ctx, "could not sort groups", slog.Any("error", err))
		b.send(ctx, job.chat, msgGroupsFailed, nil)
		b.previewImport(ctx, job)
		return
	}
	if len(sum.Proposals) == 0 && sum.Doubts == 0 {
		b.send(ctx, job.chat, groupsDoneText(sum), nil)
		b.previewImport(ctx, job)
		return
	}
	r := b.reviews.add(ctx, &review{chat: job.chat, job: job.id, supplier: job.supplier})
	text, kb := groupsSummaryView(r, sum)
	b.send(ctx, job.chat, text, kb)
}

// routeGroups dispatches the groups' buttons.
func (b *Bot) routeGroups(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	a := cb.args
	r, ok := b.reviews.get(ctx, a[0])
	if !ok {
		return "", b.editText(ctx, cc, msgGroupsExpired, nil)
	}
	switch cb.act {
	case actGroupsNext:
		return "", b.continueImport(ctx, cc, r)
	case actGroupsCreate:
		created, filed, err := b.grouping.CreateProposed(ctx, r.supplier)
		if err != nil {
			return "", err
		}
		return "", b.afterProposals(ctx, cc, r, fmt.Sprintf(msgGroupsCreated, num(created), num(filed)))
	case actGroupsDoubts:
		r.index = int(a[1])
		return "", b.showDoubt(ctx, cc, r, "")
	case actGroupAccept:
		g, err := b.grouping.Decide(ctx, a[1], 0)
		if err != nil {
			return "", err
		}
		// The group leaves the doubts: the next one takes its index.
		return msgGroupFiled, b.showDoubt(ctx, cc, r, b.filedText(ctx, g))
	case actGroupMixed:
		g, err := b.grouping.SortByName(ctx, a[1])
		if err != nil {
			return "", err
		}
		return msgGroupFiled, b.showDoubt(ctx, cc, r, fmt.Sprintf(msgGroupMixedText, esc(g.Key), num(g.Products)))
	default: // actGroupPick
		return "", b.pickForGroup(ctx, cc, r, a[1], a[2])
	}
}

// afterProposals goes on once the new categories are made: to the doubts
// left, or to the file's report.
func (b *Bot) afterProposals(ctx context.Context, cc callbackCtx, r *review, note string) error {
	sum, err := b.grouping.Summary(ctx, r.supplier)
	if err != nil {
		return err
	}
	if sum.Doubts > 0 {
		text, kb := groupsSummaryView(r, sum)
		return b.editText(ctx, cc, note+"\n\n"+text, kb)
	}
	if err := b.editText(ctx, cc, note, nil); err != nil {
		return err
	}
	return b.continueImport(ctx, cc, r)
}

// continueImport checks the review's file again, its groups now filed.
// Without a file -- groups sorted from /groups -- there is nothing to check.
func (b *Bot) continueImport(ctx context.Context, cc callbackCtx, r *review) error {
	b.reviews.remove(ctx, r.id)
	if r.job == 0 {
		return nil
	}
	job, ok := b.importJobs.get(ctx, r.job)
	if !ok {
		return b.editText(ctx, cc, msgImportExpired, nil)
	}
	if err := b.removeButtons(ctx, cc); err != nil {
		b.log.WarnContext(ctx, "could not remove group buttons", slog.Any("error", err))
	}
	b.previewImport(ctx, job)
	return nil
}

// showDoubt draws the review's doubtful group at its index, or ends the
// review past the last one.
func (b *Bot) showDoubt(ctx context.Context, cc callbackCtx, r *review, note string) error {
	doubts, err := b.grouping.Doubts(ctx, r.supplier)
	if err != nil {
		return err
	}
	b.reviews.save(ctx, r)
	if r.index >= len(doubts) {
		if err := b.editText(ctx, cc, withNote(note, msgGroupsReviewed), nil); err != nil {
			return err
		}
		return b.continueImport(ctx, cc, r)
	}
	g := doubts[r.index]
	path, err := b.grouping.SuggestionPath(ctx, g.Suggestion)
	if err != nil {
		return err
	}
	text, kb := doubtView(r, len(doubts), &g, path)
	return b.editText(ctx, cc, withNote(note, text), kb)
}

// pickForGroup is the tree under a group: a section with subcategories
// opens them, a leaf files the group there.
func (b *Bot) pickForGroup(ctx context.Context, cc callbackCtx, r *review, groupID, categoryID int64) error {
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		return err
	}
	g, err := b.grouping.Get(ctx, groupID)
	if err != nil {
		return err
	}
	if categoryID == 0 || hasSubcategories(tree, categoryID) {
		return b.editText(ctx, cc, fmt.Sprintf(msgGroupPick, esc(g.Key), num(g.Products)),
			groupPickKeyboard(r, groupID, categoryID, tree))
	}
	if g, err = b.grouping.Decide(ctx, groupID, categoryID); err != nil {
		return err
	}
	if r.supplier == 0 {
		// From /group: one group, no review to go on with.
		b.reviews.remove(ctx, r.id)
		return b.editText(ctx, cc, b.filedText(ctx, g), nil)
	}
	return b.showDoubt(ctx, cc, r, b.filedText(ctx, g))
}

// filedText says where a group went.
func (b *Bot) filedText(ctx context.Context, g *grouping.Group) string {
	path := ""
	if tree, err := b.cats.Tree(ctx); err == nil {
		if c, ok := findCategory(tree, g.CategoryID); ok {
			path = string(pathOf(tree, c))
		}
	}
	return fmt.Sprintf(msgGroupFiledText, esc(g.Key), esc(path), num(g.Products))
}

// handleGroupsMessage takes /groups and /group. It reports whether the
// message was one of them.
func (b *Bot) handleGroupsMessage(ctx context.Context, m *telegram.Message) bool {
	chat := m.Chat.ID
	if _, ok := commandArg(m.Text, cmdGroups); ok {
		all, err := b.imports.Suppliers(ctx)
		if err != nil {
			b.fail(ctx, chat, "list suppliers", err)
			return true
		}
		shown := false
		for _, sup := range all {
			sum, err := b.grouping.Summary(ctx, sup.ID)
			if err != nil {
				b.fail(ctx, chat, "sum groups up", err)
				return true
			}
			if sum.Doubts == 0 && len(sum.Proposals) == 0 {
				continue
			}
			shown = true
			r := b.reviews.add(ctx, &review{chat: chat, supplier: sup.ID})
			text, kb := groupsSummaryView(r, sum)
			b.send(ctx, chat, fmt.Sprintf("🏭 <b>%s</b>\n\n", esc(sup.Name))+text, kb)
		}
		if !shown {
			b.send(ctx, chat, msgGroupsNone, nil)
		}
		return true
	}
	arg, ok := commandArg(m.Text, cmdGroup)
	if !ok {
		return false
	}
	if arg == "" {
		b.send(ctx, chat, msgGroupHelp, nil)
		return true
	}
	found, err := b.grouping.Find(ctx, arg)
	if err != nil {
		b.fail(ctx, chat, "find groups", err)
		return true
	}
	if len(found) == 0 {
		b.send(ctx, chat, fmt.Sprintf(msgGroupNotFound, esc(arg)), nil)
		return true
	}
	tree, err := b.cats.Tree(ctx)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return true
	}
	r := b.reviews.add(ctx, &review{chat: chat})
	text, kb := foundGroupsView(r, found, tree)
	b.send(ctx, chat, text, kb)
	return true
}

// groupPickKeyboard lists sections, or a section's subcategories, for a
// group.
func groupPickKeyboard(r *review, groupID, section int64, tree []categories.Category) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for _, c := range tree {
		if c.ParentID == section {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(c.Title, actGroupPick, r.id, groupID, c.ID)})
		}
	}
	if section != 0 {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Розділи", actGroupPick, r.id, groupID, 0)})
	}
	if r.supplier != 0 {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Назад", actGroupsDoubts, r.id, int64(r.index))})
	}
	return kb
}
