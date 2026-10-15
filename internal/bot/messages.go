package bot

import "github.com/aristodem96/ai_shop_bot/internal/telegram"

// Texts of the reply keyboard buttons. A press sends the text as an ordinary
// message, so the router matches on these exact strings.
const (
	btnCatalog = "🛍 Каталог"
	btnCart    = "🛒 Кошик"
	btnHelp    = "❓ Допомога"
)

// Commands is the menu registered with Telegram.
var Commands = []telegram.Command{
	{Command: "start", Description: "Почати"},
	{Command: "catalog", Description: "Каталог"},
	{Command: "cart", Description: "Кошик"},
	{Command: "help", Description: "Як користуватися"},
}

const (
	msgWelcome = `Привіт! Я консультант магазину одягу 👋

Обирайте категорію в каталозі, гортайте картки товарів і додавайте потрібний розмір у кошик.`

	msgHelp = `<b>Як користуватися</b>

🛍 <b>Каталог</b> — категорії та товари з фото, цінами й наявними розмірами.
🛒 <b>У кошик</b> — на картці товару: оберіть розмір, і він потрапить у кошик.
🔁 <b>Схоже</b> — альтернативи того ж типу, близькі за стилем і ціною.
🛒 <b>Кошик</b> — змінити кількість, прибрати позицію або очистити.`

	// Free text is for the AI consultant, which arrives in stage 2.
	msgFreeTextSoon = `Скоро я розумітиму звичайні повідомлення на кшталт «тепла куртка до 3000 грн, розмір M» 🙂

Поки що оберіть категорію:`

	msgPrivateOnly = "Я працюю лише в особистих повідомленнях — напишіть мені напряму."

	msgCategories   = "<b>Каталог</b>\nОберіть категорію:"
	msgCartEmpty    = "🛒 Кошик порожній.\n\nЗазирніть у каталог — там є з чого обрати."
	msgNoSimilar    = "Схожих товарів у наявності зараз немає."
	msgCartCleared  = "Кошик очищено"
	msgStaleButton  = "Ця кнопка застаріла. Відкрийте каталог ще раз."
	msgGone         = "Цей товар більше недоступний."
	msgOutOfStock   = "На жаль, цього розміру більше немає в наявності."
	msgQtyLimit     = "Більше цього розміру в одне замовлення додати не можна."
	msgCartFull     = "У кошику забагато позицій. Приберіть щось, щоб додати нове."
	msgInternalFail = "Щось пішло не так. Спробуйте ще раз за хвилину."
)
