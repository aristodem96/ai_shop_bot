package bot

import (
	"encoding/json"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// The managers' dialogs go to Redis as JSON. Their fields are the bot's own
// business, so each has a mirror with exported fields for the encoding,
// rather than exporting them to the whole package's callers.

func (j *importJob) jobID() int64             { return j.id }
func (j *importJob) setJobID(id int64)        { j.id = id }
func (j *importJob) createdAt() time.Time     { return j.created }
func (j *importJob) setCreatedAt(t time.Time) { j.created = t }
func (j *matchJob) jobID() int64              { return j.id }
func (j *matchJob) setJobID(id int64)         { j.id = id }
func (j *matchJob) createdAt() time.Time      { return j.created }
func (j *matchJob) setCreatedAt(t time.Time)  { j.created = t }
func (d *carDraft) jobID() int64              { return d.id }
func (d *carDraft) setJobID(id int64)         { d.id = id }
func (d *carDraft) createdAt() time.Time      { return d.created }
func (d *carDraft) setCreatedAt(t time.Time)  { d.created = t }

type importJobJSON struct {
	ID      int64            `json:"id"`
	Chat    int64            `json:"chat"`
	Dir     string           `json:"dir"`
	Path    string           `json:"path"`
	Profile string           `json:"profile"`
	Cars    bool             `json:"cars"`
	Created time.Time        `json:"created"`
	Report  *importer.Report `json:"report,omitempty"`
}

// MarshalJSON writes the importJob for Redis.
func (j *importJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(importJobJSON{ID: j.id, Chat: j.chat, Dir: j.dir, Path: j.path, Profile: j.profile,
		Cars: j.cars, Created: j.created, Report: j.report})
}

// UnmarshalJSON reads the importJob back.
func (j *importJob) UnmarshalJSON(b []byte) error {
	var x importJobJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*j = importJob{id: x.ID, chat: x.Chat, dir: x.Dir, path: x.Path, profile: x.Profile, cars: x.Cars,
		created: x.Created, report: x.Report}
	return nil
}

type matchItemJSON struct {
	importer.UnknownVehicle
	Suggested []int64 `json:"suggested,omitempty"`
	Note      string  `json:"note,omitempty"`
}

type matchJobJSON struct {
	ID        int64            `json:"id"`
	ImportJob int64            `json:"import_job"`
	Chat      int64            `json:"chat"`
	Profile   string           `json:"profile"`
	Items     []matchItemJSON  `json:"items"`
	Idx       int              `json:"idx"`
	Models    []importer.Model `json:"models"`
	AIError   bool             `json:"ai_error,omitempty"`
	Matched   int              `json:"matched"`
	Skipped   int              `json:"skipped"`
	Created   time.Time        `json:"created"`
}

// MarshalJSON writes the matchJob for Redis.
func (j *matchJob) MarshalJSON() ([]byte, error) {
	items := make([]matchItemJSON, len(j.items))
	for i, it := range j.items {
		items[i] = matchItemJSON{UnknownVehicle: it.UnknownVehicle, Suggested: it.suggested, Note: it.note}
	}
	return json.Marshal(matchJobJSON{ID: j.id, ImportJob: j.importJob, Chat: j.chat, Profile: j.profile, Items: items,
		Idx: j.idx, Models: j.models, AIError: j.aiError, Matched: j.matched, Skipped: j.skipped, Created: j.created})
}

// UnmarshalJSON reads the matchJob back.
func (j *matchJob) UnmarshalJSON(b []byte) error {
	var x matchJobJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	items := make([]matchItem, len(x.Items))
	for i, it := range x.Items {
		items[i] = matchItem{UnknownVehicle: it.UnknownVehicle, suggested: it.Suggested, note: it.Note}
	}
	*j = matchJob{id: x.ID, importJob: x.ImportJob, chat: x.Chat, profile: x.Profile, items: items, idx: x.Idx,
		models: x.Models, aiError: x.AIError, matched: x.Matched, skipped: x.Skipped, created: x.Created}
	return nil
}

type carDraftJSON struct {
	ID       int64              `json:"id"`
	Chat     int64              `json:"chat"`
	Name     string             `json:"name"`
	Examples []string           `json:"examples,omitempty"`
	Match    int64              `json:"match,omitempty"`
	Item     int                `json:"item,omitempty"`
	Vehicles []importer.Vehicle `json:"vehicles,omitempty"`
	Dropped  map[int]bool       `json:"dropped,omitempty"`
	Note     string             `json:"note,omitempty"`
	Rejected []string           `json:"rejected,omitempty"`
	Created  time.Time          `json:"created"`
}

// MarshalJSON writes the carDraft for Redis.
func (d *carDraft) MarshalJSON() ([]byte, error) {
	return json.Marshal(carDraftJSON{ID: d.id, Chat: d.chat, Name: d.name, Examples: d.examples, Match: d.match,
		Item: d.item, Vehicles: d.vehicles, Dropped: d.dropped, Note: d.note, Rejected: d.rejected, Created: d.created})
}

// UnmarshalJSON reads the carDraft back.
func (d *carDraft) UnmarshalJSON(b []byte) error {
	var x carDraftJSON
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	*d = carDraft{id: x.ID, chat: x.Chat, name: x.Name, examples: x.Examples, match: x.Match, item: x.Item,
		vehicles: x.Vehicles, dropped: x.Dropped, note: x.Note, rejected: x.Rejected, created: x.Created}
	if d.dropped == nil {
		d.dropped = make(map[int]bool)
	}
	return nil
}
