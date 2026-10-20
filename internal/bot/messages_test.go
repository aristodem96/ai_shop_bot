package bot

import (
	"testing"
	"unicode/utf16"

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

// Telegram refuses longer ones and the bot would start without them.
func TestDescriptionsFitTelegram(t *testing.T) {
	assert.LessOrEqual(t, len(utf16.Encode([]rune(Description))), 512)
	assert.LessOrEqual(t, len(utf16.Encode([]rune(ShortDescription))), 120)
	assert.LessOrEqual(t, len(utf16.Encode([]rune(msgPlaceholder))), 64)
}

// The managers' help is one message: Telegram takes 4096 characters.
func TestAdminHelpFitsOneMessage(t *testing.T) {
	assert.LessOrEqual(t, len(utf16.Encode([]rune(msgAdminHelp))), 4096)
	for _, c := range adminCommands {
		if c.Command != "admin" {
			assert.Contains(t, msgAdminHelp, "/"+c.Command, "every managers' command is explained")
		}
	}
}
