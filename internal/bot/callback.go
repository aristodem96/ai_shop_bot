package bot

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Callback data is what an inline button sends back when pressed: a short
// action code and its numeric arguments, joined by colons, e.g. "p:12".
//
// Telegram caps it at 64 bytes and stores it on the user's phone, so it is
// treated as untrusted input: it names what was pressed, never a price or a
// quantity. The handler looks everything else up again, which is also why an
// old button still does the right thing after the catalogue has changed.
type action string

const (
	actCategories     action = "cats" // edit the message into the category menu
	actCategory       action = "c"    // c:<category>:<page> edit into a product page
	actCategoryN      action = "cn"   // cn:<category> send a product page as a new message
	actCategoryAll    action = "ca"   // ca:<section>:<page> every product of a section
	actProduct        action = "p"    // p:<product> send a product card
	actAdd            action = "a"    // a:<product> add one unit to the cart
	actAnalogues      action = "s"    // s:<product> send interchangeable parts
	actCart           action = "cart" // send the cart as a new message
	actInc            action = "ci"   // ci:<product> one more unit
	actDec            action = "cd"   // cd:<product> one unit less
	actDel            action = "cr"   // cr:<product> remove the line
	actClear          action = "cx"   // empty the cart
	actForgetCar      action = "gx"   // forget the car
	actKit            action = "tk"   // send the maintenance kit for the buyer's car
	actKitAdd         action = "ta"   // add the whole kit to the cart
	actCheckout       action = "co"   // start the checkout
	actPay            action = "cp"   // place the order and get the payment link
	actEditContacts   action = "ce"   // type the contacts again
	actCancelCheckout action = "cq"   // leave the checkout
	actManage         action = "om"   // om:<order>:<move> a manager's button
	actImportApply    action = "ia"   // ia:<job>:<force> apply a checked file
	actImportCancel   action = "ix"   // ix:<job> drop an uploaded file
	actSetupField     action = "sf"   // sf:<job>:<field> which column a field of a new file is
	actSetupColumn    action = "sc"   // sc:<job>:<field>:<column, -1 none> read the field from it
	actSetupNext      action = "sn"   // sn:<job> the columns are right
	actSetupLead      action = "sl"   // sl:<job>:<days> a file without stock is on order, this many days
	actSetupMarkup    action = "sm"   // sm:<job>:<percent> added to the file's prices
	actSetupSupplier  action = "ss"   // ss:<job>:<supplier, 0 new> whose the file is
	actSupplierOpen   action = "uo"   // uo:<supplier> its screen; 0 the list
	actSupplierMarkup action = "um"   // um:<supplier>:<percent, -1 to choose>
	actSupplierLead   action = "ul"   // ul:<supplier>:<days, -1 to choose>
	actSupplierDelete action = "ud"   // ud:<supplier>:<1 confirmed>
	actGroupsNext     action = "gp"   // gp:<review> on to the file's report
	actGroupsCreate   action = "gn"   // gn:<review> create the proposed categories
	actGroupsDoubts   action = "gd"   // gd:<review>:<index> the doubtful group at index
	actGroupAccept    action = "ga"   // ga:<review>:<group> file it as the model guessed
	actGroupPick      action = "gk"   // gk:<review>:<group>:<category, 0 sections> pick its category
	actGroupMixed     action = "gm"   // gm:<review>:<group> different kinds of products: sort by name
	actCatOpen        action = "ko"   // ko:<category> a category's screen; 0 the sections
	actCatNew         action = "kn"   // kn:<section or 0> a new subcategory, or section: asks its title
	actCatRename      action = "kr"   // kr:<category> asks the new title
	actCatAddWord     action = "kw"   // kw:<category> asks the word
	actCatRemoveWord  action = "kx"   // kx:<word> plan removing it
	actCatDelete      action = "kd"   // kd:<category> plan deleting it
	actCatPick        action = "kp"   // kp:<job>:<category> the category of a /word, 0 the sections
	actCatApply       action = "ka"   // ka:<job> apply the planned change
	actCatCancel      action = "kc"   // kc:<job> drop it
	actMatchStart     action = "vm"   // vm:<import job> map the report's unknown vehicle names
	actMatchPick      action = "vp"   // vp:<session>:<item>:<model, 0 for all suggested> confirm
	actMatchMakes     action = "vo"   // vo:<session> pick by hand: the makes
	actMatchMake      action = "vk"   // vk:<session>:<make index> the make's models
	actMatchBack      action = "vb"   // vb:<session> back to the current name
	actMatchSkip      action = "vs"   // vs:<session>:<item> leave the name unmapped
	actMatchAdd       action = "va"   // va:<session>:<item> add the vehicle to the directory
	actCarAdd         action = "cy"   // cy:<draft> write the kept vehicles
	actCarToggle      action = "cw"   // cw:<draft>:<index> drop or keep one vehicle
	actCarText        action = "ct"   // ct:<draft> correct, or type, by reply
	actCarBack        action = "cb"   // cb:<draft> back to matching, or drop the draft
	actMatchStop      action = "ve"   // ve:<session> end the session
	actCarDelete      action = "vd"   // vd:<alias> forget a mapping and its fitments
	actRequest        action = "rq"   // rq:<catalogue part> request a part on order: how many
	actRequestQty     action = "rn"   // rn:<catalogue part>:<qty> send the request to the managers
	actRequestFound   action = "rf"   // rf:<request> a manager found it: ask for the price
	actRequestNone    action = "rx"   // rx:<request> a manager did not find it
	actRequestBuy     action = "ro"   // ro:<request> the buyer takes the quote: to the cart
	actRequestDecline action = "rd"   // rd:<request> the buyer turns the quote down
	actNoop           action = "x"    // a label that only looks like a button
)

// arity is how many arguments each action takes. Parsing checks it, so a
// handler can index the arguments without guarding against short input.
var arity = map[action]int{
	actCategories: 0, actCategory: 2, actCategoryN: 1, actCategoryAll: 2, actProduct: 1,
	actAdd: 1, actAnalogues: 1, actCart: 0, actInc: 1, actDec: 1, actDel: 1,
	actClear: 0, actForgetCar: 0, actKit: 0, actKitAdd: 0, actNoop: 0,
	actCheckout: 0, actPay: 0, actEditContacts: 0, actCancelCheckout: 0, actManage: 2,
	actImportApply: 2, actImportCancel: 1,
	actSetupField: 2, actSetupColumn: 3, actSetupNext: 1, actSetupLead: 2, actSetupMarkup: 2, actSetupSupplier: 2,
	actSupplierOpen: 1, actSupplierMarkup: 2, actSupplierLead: 2, actSupplierDelete: 2,
	actCatOpen: 1, actCatNew: 1, actCatRename: 1, actCatAddWord: 1, actCatRemoveWord: 1, actCatDelete: 1,
	actCatPick: 2, actCatApply: 1, actCatCancel: 1,
	actGroupsNext: 1, actGroupsCreate: 1, actGroupsDoubts: 2, actGroupAccept: 2, actGroupPick: 3, actGroupMixed: 2,
	actMatchStart: 1, actMatchPick: 3, actMatchMakes: 1, actMatchMake: 2, actMatchBack: 1,
	actMatchSkip: 2, actMatchStop: 1, actCarDelete: 1, actMatchAdd: 2,
	actCarAdd: 1, actCarToggle: 2, actCarText: 1, actCarBack: 1,
	actRequest: 1, actRequestQty: 2, actRequestFound: 1, actRequestNone: 1, actRequestBuy: 1, actRequestDecline: 1,
}

// maxCallbackData is Telegram's limit on callback_data, in bytes.
const maxCallbackData = 64

var errBadCallback = errors.New("bot: malformed callback data")

type callback struct {
	act  action
	args []int64
}

func encode(act action, args ...int64) string {
	var b strings.Builder
	b.WriteString(string(act))
	for _, a := range args {
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(a, 10))
	}
	return b.String()
}

func parseCallback(data string) (callback, error) {
	if len(data) > maxCallbackData {
		return callback{}, errBadCallback
	}
	parts := strings.Split(data, ":")
	act := action(parts[0])
	n, ok := arity[act]
	if !ok || len(parts)-1 != n {
		return callback{}, fmt.Errorf("%w: %q", errBadCallback, data)
	}

	cb := callback{act: act, args: make([]int64, n)}
	for i, p := range parts[1:] {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return callback{}, fmt.Errorf("%w: %q", errBadCallback, data)
		}
		cb.args[i] = v
	}
	return cb, nil
}
