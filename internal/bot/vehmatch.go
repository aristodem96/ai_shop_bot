package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// VehicleMatching maps a price list's unknown vehicle names to the
// catalogue's models: a model suggests, the owner confirms each by button.
type VehicleMatching interface {
	CanSuggest() bool
	SuggestVehicles(ctx context.Context, unknown []importer.UnknownVehicle) (map[string]importer.VehicleSuggestion, error)
	VehicleModels(ctx context.Context) ([]importer.Model, error)
	AddVehicleAlias(ctx context.Context, profile, name string, modelIDs []int64) error
	CanPropose() bool
	ProposeVehicles(ctx context.Context, req importer.ProposeRequest) (*importer.VehicleProposal, error)
	AddVehicles(ctx context.Context, vs []importer.Vehicle) ([]int64, error)
	VehicleAliases(ctx context.Context) ([]importer.VehicleAlias, error)
	RemoveVehicleAlias(ctx context.Context, id int64) (*importer.VehicleAlias, error)
}

// cmdCars lists the confirmed mappings.
const cmdCars = "/cars"

// maxMatchItems bounds one session: the names with the most rows first, the
// ones that matter, and a session one can finish.
const maxMatchItems = 40

// matchItem is one unknown name and what the model proposed for it.
type matchItem struct {
	importer.UnknownVehicle
	suggested []int64
	note      string
}

// matchJob walks the owner through a price list's unknown names.
type matchJob struct {
	id        int64
	importJob int64
	chat      int64
	profile   string
	items     []matchItem
	idx       int
	models    []importer.Model
	aiError   bool // the model was asked and failed; picking by hand still works
	matched   int
	skipped   int
	created   time.Time
}

func (j *matchJob) current() *matchItem { return &j.items[j.idx] }

func (j *matchJob) model(id int64) (importer.Model, bool) {
	for _, m := range j.models {
		if m.ID == id {
			return m, true
		}
	}
	return importer.Model{}, false
}

// UseVehicleMatching turns on mapping vehicle names from import reports, and
// /cars.
func (b *Bot) UseVehicleMatching(m VehicleMatching) {
	b.matching = m
	b.matchJobs = newJobs[matchJob](importTTL, b.dialogs("dialog:match:", importTTL), nil, b.log)
	b.carDrafts = newJobs[carDraft](importTTL, b.dialogs("dialog:car:", importTTL), b.dialogs("dialog:car-reply:", importTTL), b.log)
}

// routeMatch dispatches the vehicle matching buttons.
func (b *Bot) routeMatch(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	a := cb.args
	switch cb.act {
	case actMatchStart:
		return b.startMatch(ctx, cc, a[0])
	case actMatchPick:
		return b.pickMatch(ctx, cc, a[0], a[1], a[2])
	case actMatchMakes:
		return b.matchMakes(ctx, cc, a[0])
	case actMatchMake:
		return b.matchModels(ctx, cc, a[0], a[1])
	case actMatchBack:
		return b.backToMatch(ctx, cc, a[0])
	case actMatchSkip:
		return b.skipMatch(ctx, cc, a[0], a[1])
	case actMatchAdd:
		return b.addFromMatch(ctx, cc, a[0], a[1])
	case actMatchStop:
		return b.stopMatch(ctx, cc, a[0])
	default:
		return b.deleteCar(ctx, cc, a[0])
	}
}

// startMatch is the "map vehicles" button under an import preview.
func (b *Bot) startMatch(ctx context.Context, cc callbackCtx, importID int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	ij, ok := b.importJobs.get(ctx, importID)
	if !ok || ij.report == nil || ij.cars {
		return msgImportExpired, b.removeButtons(ctx, cc)
	}
	unknown := ij.report.Unknown()
	if len(unknown) == 0 {
		return msgMatchNothing, nil
	}
	unknown = unknown[:min(len(unknown), maxMatchItems)]
	if err := b.removeButtons(ctx, cc); err != nil {
		b.log.WarnContext(ctx, "could not remove import buttons", slog.Any("error", err))
	}

	models, err := b.matching.VehicleModels(ctx)
	if err != nil {
		return "", err
	}
	job := &matchJob{importJob: importID, chat: cc.chat, profile: ij.profile, models: models, created: time.Now()}
	var suggestions map[string]importer.VehicleSuggestion
	if b.matching.CanSuggest() {
		b.send(ctx, cc.chat, fmt.Sprintf(msgMatchThinking, num(len(unknown))), nil)
		b.typing(ctx, cc.chat)
		if suggestions, err = b.matching.SuggestVehicles(ctx, unknown); err != nil {
			// Picking by hand works without the model; its failure costs
			// the suggestions, not the session.
			b.log.WarnContext(ctx, "could not suggest vehicle matches", slog.Any("error", err))
			job.aiError = true
		}
	}
	for _, u := range unknown {
		s := suggestions[u.Name]
		job.items = append(job.items, matchItem{UnknownVehicle: u, suggested: s.ModelIDs, note: s.Note})
	}
	b.matchJobs.add(ctx, job)
	text, kb := matchItemView(job, b.matching.CanSuggest())
	b.send(ctx, cc.chat, text, kb)
	return "", nil
}

// pickMatch confirms models for the current name: one, or with model zero
// all the suggested ones. item is the name the button was shown with: a
// second tap on it, landing after the session moved on, must not confirm
// the next name with the same choice.
func (b *Bot) pickMatch(ctx context.Context, cc callbackCtx, id, item, modelID int64) (string, error) {
	job, err := b.matchItemStep(ctx, cc, id, item)
	if err != nil || job == nil {
		return msgMatchStale, err
	}
	cur := job.current()
	ids := cur.suggested
	if modelID != 0 {
		if _, ok := job.model(modelID); !ok {
			return "", errBadCallback
		}
		ids = []int64{modelID}
	}
	if len(ids) == 0 {
		return "", errBadCallback
	}
	if err := b.matching.AddVehicleAlias(ctx, job.profile, cur.Name, ids); err != nil {
		return "", err
	}
	job.matched++
	return "✅ " + cur.Name, b.nextMatch(ctx, cc, job)
}

func (b *Bot) skipMatch(ctx context.Context, cc callbackCtx, id, item int64) (string, error) {
	job, err := b.matchItemStep(ctx, cc, id, item)
	if err != nil || job == nil {
		return msgMatchStale, err
	}
	job.skipped++
	return "", b.nextMatch(ctx, cc, job)
}

// matchMakes opens picking by hand: the makes first.
func (b *Bot) matchMakes(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	job, err := b.matchStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	return "", b.editText(ctx, cc, fmt.Sprintf(msgMatchPickMake, esc(job.current().Name)), makesKeyboard(job))
}

// matchModels lists the models of the make at index mk.
func (b *Bot) matchModels(ctx context.Context, cc callbackCtx, id, mk int64) (string, error) {
	job, err := b.matchStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	makes := modelMakes(job.models)
	if mk < 0 || mk >= int64(len(makes)) {
		return "", errBadCallback
	}
	return "", b.editText(ctx, cc, fmt.Sprintf(msgMatchPickModel, esc(job.current().Name), esc(makes[mk])),
		modelsKeyboard(job, makes[mk]))
}

// backToMatch returns from picking by hand to the current name.
func (b *Bot) backToMatch(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	job, err := b.matchStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	text, kb := matchItemView(job, b.matching.CanSuggest())
	return "", b.editText(ctx, cc, text, kb)
}

// stopMatch ends the session early; what was confirmed stays.
func (b *Bot) stopMatch(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	job, err := b.matchStep(ctx, cc, id)
	if err != nil || job == nil {
		return "", err
	}
	return "", b.finishMatch(ctx, cc, job)
}

func (b *Bot) nextMatch(ctx context.Context, cc callbackCtx, job *matchJob) error {
	job.idx++
	if job.idx >= len(job.items) {
		return b.finishMatch(ctx, cc, job)
	}
	b.matchJobs.save(ctx, job)
	text, kb := matchItemView(job, b.matching.CanSuggest())
	return b.editText(ctx, cc, text, kb)
}

// finishMatch sums the session up and checks the file again: the new
// mappings turn into fitments, and the new report has the Apply button.
func (b *Bot) finishMatch(ctx context.Context, cc callbackCtx, job *matchJob) error {
	b.matchJobs.remove(ctx, job.id)
	// Stopped early, the names not reached count as skipped too.
	skipped := job.skipped + max(len(job.items)-job.idx, 0)
	if err := b.editText(ctx, cc, fmt.Sprintf(msgMatchDone, num(job.matched), num(skipped)), nil); err != nil {
		return err
	}
	ij, ok := b.importJobs.get(ctx, job.importJob)
	if !ok {
		b.send(ctx, cc.chat, msgMatchFileGone, nil)
		return nil
	}
	b.previewImport(ctx, ij)
	return nil
}

// matchItemStep is matchStep for a button about one name: it finds nothing
// when the session has moved past that name.
func (b *Bot) matchItemStep(ctx context.Context, cc callbackCtx, id, item int64) (*matchJob, error) {
	job, err := b.matchStep(ctx, cc, id)
	if err != nil || job == nil || int64(job.idx) != item {
		return nil, err
	}
	return job, nil
}

// matchStep fetches a session for a button; a stale one loses its buttons.
func (b *Bot) matchStep(ctx context.Context, cc callbackCtx, id int64) (*matchJob, error) {
	if !b.isAdmin(cc.chat) {
		return nil, errBadCallback
	}
	job, ok := b.matchJobs.get(ctx, id)
	if !ok {
		return nil, b.editText(ctx, cc, msgMatchExpired, nil)
	}
	return job, nil
}

// carsList is /cars: the confirmed mappings with a delete button each.
func (b *Bot) carsList(ctx context.Context) (string, *telegram.InlineKeyboard) {
	aliases, err := b.matching.VehicleAliases(ctx)
	if err != nil {
		b.log.ErrorContext(ctx, "list vehicle aliases failed", slog.Any("error", err))
		return msgInternalFail, nil
	}
	return carsListView(aliases)
}

// deleteCar removes a mapping and its fitments.
func (b *Bot) deleteCar(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	a, err := b.matching.RemoveVehicleAlias(ctx, id)
	if errors.Is(err, importer.ErrNoAlias) {
		text, kb := b.carsList(ctx)
		return msgWordGone, b.editText(ctx, cc, text, kb)
	}
	if err != nil {
		return "", err
	}
	text, kb := b.carsList(ctx)
	if err := b.editText(ctx, cc, text, kb); err != nil {
		return "", err
	}
	return fmt.Sprintf(msgCarRemoved, a.Name, a.Model, a.Fitments), nil
}

// modelMakes lists the makes in the order models come, sorted by make.
func modelMakes(models []importer.Model) []string {
	var makes []string
	for _, m := range models {
		if !slices.Contains(makes, m.Make) {
			makes = append(makes, m.Make)
		}
	}
	slices.Sort(makes)
	return makes
}
