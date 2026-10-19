package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The owner's private chat gets the buyer's menu and the managers' at once;
// Telegram refuses a menu that names one command twice.
func TestAdminMenuNamesEachCommandOnce(t *testing.T) {
	for _, chat := range []int64{42, -100} {
		seen := map[string]bool{}
		for _, c := range AdminMenu(chat) {
			assert.False(t, seen[c.Command], "/%s twice in the menu of chat %d", c.Command, chat)
			seen[c.Command] = true
		}
	}
}
