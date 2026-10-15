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
	actCategories action = "cats" // edit the message into the category menu
	actCategory   action = "c"    // c:<category>:<page> edit into a product page
	actCategoryN  action = "cn"   // cn:<category> send a product page as a new message
	actProduct    action = "p"    // p:<product> send a product card
	actAdd        action = "a"    // a:<product> add one unit to the cart
	actAnalogues  action = "s"    // s:<product> send interchangeable parts
	actCart       action = "cart" // send the cart as a new message
	actInc        action = "ci"   // ci:<product> one more unit
	actDec        action = "cd"   // cd:<product> one unit less
	actDel        action = "cr"   // cr:<product> remove the line
	actClear      action = "cx"   // empty the cart
	actMakes      action = "gk"   // edit into the make menu
	actMake       action = "gm"   // gm:<vehicle> edit into the vehicles of that vehicle's make
	actSetCar     action = "gs"   // gs:<vehicle> choose the car
	actForgetCar  action = "gx"   // forget the car
	actNoop       action = "x"    // a label that only looks like a button
)

// arity is how many arguments each action takes. Parsing checks it, so a
// handler can index the arguments without guarding against short input.
var arity = map[action]int{
	actCategories: 0, actCategory: 2, actCategoryN: 1, actProduct: 1,
	actAdd: 1, actAnalogues: 1, actCart: 0, actInc: 1, actDec: 1, actDel: 1,
	actClear: 0, actMakes: 0, actMake: 1, actSetCar: 1, actForgetCar: 0, actNoop: 0,
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
