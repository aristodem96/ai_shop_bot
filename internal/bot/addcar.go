package bot

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Adding a vehicle to the directory from the chat: a draft of entries -- a
// model's proposal, or typed by hand -- that the owner trims and corrects
// before anything is written. From a matching session the new vehicle is
// mapped to the supplier's name at once and the session moves on; /car adds
// one outside any import.

// cmdCar adds a vehicle described in words.
const cmdCar = "/car"

// carDraft is a vehicle being added.
type carDraft struct {
	id   int64
	chat int64
	// name is the vehicle as the supplier or the owner wrote it; examples
	// are product names that mention it.
	name     string
	examples []string
	// match is the matching session the draft came from, zero for /car;
	// item is the name in it.
	match    int64
	item     int
	vehicles []importer.Vehicle
	dropped  map[int]bool
	note     string
	rejected []string
	created  time.Time
}

func (d *carDraft) kept() []importer.Vehicle {
	var out []importer.Vehicle
	for i, v := range d.vehicles {
		if !d.dropped[i] {
			out = append(out, v)
		}
	}
	return out
}

// addFromMatch is "add to the directory" on a name of a matching session.
// The session's message turns into the draft.
func (b *Bot) addFromMatch(ctx context.Context, cc callbackCtx, id, item int64) (string, error) {
	job, err := b.matchItemStep(ctx, cc, id, item)
	if err != nil || job == nil {
		return msgMatchStale, err
	}
	cur := job.current()
	d := b.carDrafts.add(ctx, &carDraft{chat: cc.chat, name: cur.Name, examples: cur.Examples, match: job.id, item: job.idx,
		dropped: make(map[int]bool)})
	return "", b.proposeCar(ctx, cc, d, "")
}

// startCar is /car: a vehicle described in words, outside any import.
func (b *Bot) startCar(ctx context.Context, chat int64, text string) {
	if text == "" {
		help := msgCarHelpManual
		if b.matching.CanPropose() {
			help = msgCarHelp
		}
		b.send(ctx, chat, help, nil)
		return
	}
	d := b.carDrafts.add(ctx, &carDraft{chat: chat, name: text, dropped: make(map[int]bool)})
	if !b.matching.CanPropose() {
		b.takeCarText(ctx, chat, d, text)
		return
	}
	if err := b.proposeCar(ctx, callbackCtx{chat: chat}, d, ""); err != nil {
		b.fail(ctx, chat, "propose a vehicle", err)
	}
}

// proposeCar asks the model for the draft's entries, with the owner's
// correction when there is one, and shows the draft. Without a model it
// asks for the vehicle typed by hand.
func (b *Bot) proposeCar(ctx context.Context, cc callbackCtx, d *carDraft, correction string) error {
	if !b.matching.CanPropose() {
		return b.askCarText(ctx, cc, d)
	}
	b.typing(ctx, cc.chat)
	p, err := b.matching.ProposeVehicles(ctx, importer.ProposeRequest{
		Name: d.name, Examples: d.examples, Correction: correction, Previous: d.kept(),
	})
	if err != nil {
		b.log.WarnContext(ctx, "could not propose vehicles", slog.Any("error", err))
		d.note = msgCarNoModel
	} else {
		d.vehicles, d.note, d.rejected = p.Vehicles, p.Note, p.Rejected
		d.dropped = make(map[int]bool)
	}
	b.carDrafts.save(ctx, d)
	text, kb := carDraftView(d, b.matching.CanPropose())
	return b.editText(ctx, cc, text, kb)
}

// askCarText asks for a reply: a correction to the model, or the vehicle
// typed by hand.
func (b *Bot) askCarText(ctx context.Context, cc callbackCtx, d *carDraft) error {
	b.carDrafts.await(ctx, cc.chat, d.id)
	question := msgCarManual
	if b.matching.CanPropose() && len(d.vehicles) > 0 {
		question = msgCarCorrect
	}
	return b.tg.SendMessage(ctx, cc.chat, question, &telegram.ForceReply{ForceReply: true})
}

// takeCarText handles the reply askCarText asked for.
func (b *Bot) takeCarText(ctx context.Context, chat int64, d *carDraft, text string) {
	cc := callbackCtx{chat: chat}
	if b.matching.CanPropose() {
		if err := b.proposeCar(ctx, cc, d, text); err != nil {
			b.fail(ctx, chat, "propose a vehicle", err)
		}
		return
	}
	v, err := importer.ParseVehicleLine(text)
	if err != nil {
		b.send(ctx, chat, "❌ "+esc(err.Error()), nil)
		if err := b.askCarText(ctx, cc, d); err != nil {
			b.log.WarnContext(ctx, "could not ask for a vehicle", slog.Any("error", err))
		}
		return
	}
	d.vehicles = append(d.vehicles, v)
	b.carDrafts.save(ctx, d)
	text2, kb := carDraftView(d, false)
	b.send(ctx, chat, text2, kb)
}

// handleCarText takes /car and the replies a draft waits for. It reports
// whether the message was one of them.
func (b *Bot) handleCarText(ctx context.Context, m *telegram.Message) bool {
	chat := m.Chat.ID
	if arg, ok := commandArg(m.Text, cmdCar); ok {
		b.carDrafts.forget(ctx, chat)
		b.startCar(ctx, chat, arg)
		return true
	}
	if m.Text == "" || strings.HasPrefix(m.Text, "/") {
		return false
	}
	d, ok := b.carDrafts.awaited(ctx, chat)
	if !ok {
		return false
	}
	b.takeCarText(ctx, chat, d, m.Text)
	return true
}

func (b *Bot) routeCar(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	d, ok := b.carDrafts.get(ctx, cb.args[0])
	if !ok {
		return "", b.editText(ctx, cc, msgMatchExpired, nil)
	}
	switch cb.act {
	case actCarToggle:
		i := int(cb.args[1])
		if i < 0 || i >= len(d.vehicles) {
			return "", errBadCallback
		}
		d.dropped[i] = !d.dropped[i]
		b.carDrafts.save(ctx, d)
		text, kb := carDraftView(d, b.matching.CanPropose())
		return "", b.editText(ctx, cc, text, kb)
	case actCarText:
		return "", b.askCarText(ctx, cc, d)
	case actCarBack:
		b.carDrafts.remove(ctx, d.id)
		return "", b.backToMatchOrClose(ctx, cc, d)
	default:
		return b.addCar(ctx, cc, d)
	}
}

// addCar writes the kept vehicles. From a matching session it maps the
// supplier's name to them and moves to the next name.
func (b *Bot) addCar(ctx context.Context, cc callbackCtx, d *carDraft) (string, error) {
	kept := d.kept()
	if len(kept) == 0 {
		return msgCarNothing, nil
	}
	b.carDrafts.remove(ctx, d.id)
	ids, err := b.matching.AddVehicles(ctx, kept)
	if err != nil {
		return "", err
	}
	if d.match == 0 {
		return "", b.editText(ctx, cc, carAddedText(kept), nil)
	}
	job, ok := b.matchJobs.get(ctx, d.match)
	if !ok || job.idx != d.item {
		// The session ended or moved on meanwhile: the vehicles are in, the
		// name is mapped at its next import or session.
		return "", b.editText(ctx, cc, carAddedText(kept), nil)
	}
	if err := b.matching.AddVehicleAlias(ctx, job.profile, d.name, ids); err != nil {
		return "", err
	}
	if job.models, err = b.matching.VehicleModels(ctx); err != nil {
		return "", err
	}
	job.matched++
	return "✅ " + d.name, b.nextMatch(ctx, cc, job)
}

// backToMatchOrClose leaves a draft: back to the name it came from, or, for
// /car, closed.
func (b *Bot) backToMatchOrClose(ctx context.Context, cc callbackCtx, d *carDraft) error {
	if d.match != 0 {
		if job, ok := b.matchJobs.get(ctx, d.match); ok && job.idx == d.item {
			text, kb := matchItemView(job, b.matching.CanSuggest())
			return b.editText(ctx, cc, text, kb)
		}
	}
	return b.editText(ctx, cc, msgWordCancelled, nil)
}
