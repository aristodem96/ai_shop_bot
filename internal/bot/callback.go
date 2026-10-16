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
	actImportProfile  action = "ip"   // ip:<job>:<choice> what an uploaded file is
	actImportApply    action = "ia"   // ia:<job>:<force> apply a checked file
	actImportCancel   action = "ix"   // ix:<job> drop an uploaded file
	actWordPick       action = "wk"   // wk:<job>:<category index> file a word there, or open the section
	actWordNew        action = "wn"   // wn:<job>:<section index or -1> a new subcategory or section
	actWordBack       action = "wb"   // wb:<job> back to the sections
	actWordApply      action = "wa"   // wa:<job> store the word, move the products
	actWordCancel     action = "wx"   // wx:<job> drop the change
	actWordDelete     action = "wd"   // wd:<keyword> preview forgetting an owner's word
	actWordRemove     action = "wr"   // wr:<keyword> forget it, move its products back
	actWordKeep       action = "wz"   // wz:<keyword> keep it, back to the list
	actNoop           action = "x"    // a label that only looks like a button
)

// arity is how many arguments each action takes. Parsing checks it, so a
// handler can index the arguments without guarding against short input.
var arity = map[action]int{
	actCategories: 0, actCategory: 2, actCategoryN: 1, actCategoryAll: 2, actProduct: 1,
	actAdd: 1, actAnalogues: 1, actCart: 0, actInc: 1, actDec: 1, actDel: 1,
	actClear: 0, actForgetCar: 0, actKit: 0, actKitAdd: 0, actNoop: 0,
	actCheckout: 0, actPay: 0, actEditContacts: 0, actCancelCheckout: 0, actManage: 2,
	actImportProfile: 2, actImportApply: 2, actImportCancel: 1,
	actWordPick: 2, actWordNew: 2, actWordBack: 1, actWordApply: 1, actWordCancel: 1, actWordDelete: 1,
	actWordRemove: 1, actWordKeep: 1,
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
