package seed

// The demo catalogue. Brands are made up so the bot can be shown publicly
// without borrowing anyone's trademark.

type category struct {
	slug, title string
	chart       string
}

var categories = []category{
	{"jackets", "Куртки", "tops"},
	{"hoodies", "Худі та світшоти", "tops"},
	{"tshirts", "Футболки", "tops"},
	{"jeans", "Джинси та штани", "bottoms"},
	{"shoes", "Взуття", "shoes"},
	{"accessories", "Аксесуари", "one_size"},
}

// chartRow is one size of a chart. Zero means "not measured by this chart"
// and is stored as NULL.
type chartRow struct {
	size                 string
	chestMin, chestMax   int
	waistMin, waistMax   int
	heightMin, heightMax int
	footMin, footMax     int
}

type chart struct {
	code, title string
	rows        []chartRow
}

var charts = []chart{
	{"tops", "Верхній одяг і футболки", []chartRow{
		{size: "XS", chestMin: 82, chestMax: 87, heightMin: 158, heightMax: 164},
		{size: "S", chestMin: 88, chestMax: 93, heightMin: 164, heightMax: 170},
		{size: "M", chestMin: 94, chestMax: 99, heightMin: 170, heightMax: 176},
		{size: "L", chestMin: 100, chestMax: 105, heightMin: 176, heightMax: 182},
		{size: "XL", chestMin: 106, chestMax: 111, heightMin: 182, heightMax: 188},
		{size: "XXL", chestMin: 112, chestMax: 118, heightMin: 186, heightMax: 194},
	}},
	// Jeans are sized by waist in inches, the way denim labels are printed.
	{"bottoms", "Джинси (талія, дюйми)", []chartRow{
		{size: "28", waistMin: 70, waistMax: 74},
		{size: "30", waistMin: 75, waistMax: 79},
		{size: "32", waistMin: 80, waistMax: 84},
		{size: "34", waistMin: 85, waistMax: 89},
		{size: "36", waistMin: 90, waistMax: 95},
	}},
	{"shoes", "Взуття (довжина стопи)", []chartRow{
		{size: "37", footMin: 235, footMax: 240},
		{size: "38", footMin: 241, footMax: 246},
		{size: "39", footMin: 247, footMax: 252},
		{size: "40", footMin: 253, footMax: 258},
		{size: "41", footMin: 259, footMax: 264},
		{size: "42", footMin: 265, footMax: 270},
		{size: "43", footMin: 271, footMax: 276},
		{size: "44", footMin: 277, footMax: 282},
		{size: "45", footMin: 283, footMax: 288},
	}},
	{"one_size", "Універсальний розмір", []chartRow{{size: "ONE"}}},
}

type product struct {
	sku, category string
	name, brand   string
	color, season string
	gender        string
	styles        []string
	price         int64 // whole hryvnias
	description   string
	imageLabel    string         // English text drawn on the placeholder photo
	stock         map[string]int // overrides the generated stock per size
	excludeSizes  []string
}

var products = []product{
	// --- Jackets -------------------------------------------------------------
	{"JKT-001", "jackets", "Пуховик «Nord»", "Nord Line", "black", "winter", "unisex", []string{"casual", "outdoor"}, 3499,
		"Теплий пуховик з капюшоном, наповнювач пух/перо 90/10, тримає до −20 °C.", "Puffer Nord",
		// M holds a single unit on purpose: stage 3 races two buyers for it.
		map[string]int{"M": 1}, nil},
	{"JKT-002", "jackets", "Пуховик «Nord»", "Nord Line", "olive", "winter", "unisex", []string{"casual", "outdoor"}, 3499,
		"Теплий пуховик з капюшоном, наповнювач пух/перо 90/10, тримає до −20 °C.", "Puffer Nord", nil, nil},
	{"JKT-003", "jackets", "Парка «Polar»", "Carpathia", "khaki", "winter", "men", []string{"outdoor", "casual"}, 4299,
		"Довга парка з мембраною 10K і штучним хутром на капюшоні. Для морозу й вітру.", "Parka Polar", nil, nil},
	{"JKT-004", "jackets", "Бомбер «Pilot»", "Urban Fox", "black", "demi", "unisex", []string{"street", "casual"}, 2199,
		"Класичний нейлоновий бомбер з легким утеплювачем і трикотажними манжетами.", "Bomber Pilot", nil, nil},
	{"JKT-005", "jackets", "Вітровка «Breeze»", "Urban Fox", "blue", "summer", "unisex", []string{"sport"}, 1299,
		"Легка вітровка з водовідштовхувальним просоченням, складається в кишеню.", "Windbreaker", nil, nil},
	{"JKT-006", "jackets", "Джинсова куртка «Trucker»", "Kyiv Denim", "blue", "demi", "unisex", []string{"casual", "street"}, 1899,
		"Джинсова куртка прямого крою з щільного деніму 13 oz.", "Denim Trucker", nil, nil},
	{"JKT-007", "jackets", "Софтшел «Trail»", "Carpathia", "grey", "demi", "men", []string{"outdoor", "sport"}, 2699,
		"Софтшел з флісовою підкладкою: для походів у Карпати й прохолодного міста.", "Softshell Trail", nil, nil},
	{"JKT-008", "jackets", "Пальто «Lviv»", "Stepove", "beige", "demi", "women", []string{"classic"}, 4899,
		"Вовняне пальто прямого крою, 60% вовни. Довжина до коліна.", "Coat Lviv", nil, []string{"XXL"}},
	{"JKT-009", "jackets", "Укорочений пуховик «Puff»", "Nord Line", "white", "winter", "women", []string{"street", "casual"}, 2999,
		"Об'ємний укорочений пуховик зі стьобкою, високий комір-стійка.", "Puffer Short", nil, []string{"XXL"}},

	// --- Hoodies -------------------------------------------------------------
	{"HOD-001", "hoodies", "Худі «Basic»", "Urban Fox", "black", "all", "unisex", []string{"casual"}, 1199,
		"Щільний футер 320 г/м² з начосом, кишеня-кенгуру, подвійний капюшон.", "Hoodie Basic", nil, nil},
	{"HOD-002", "hoodies", "Худі «Basic»", "Urban Fox", "grey", "all", "unisex", []string{"casual"}, 1199,
		"Щільний футер 320 г/м² з начосом, кишеня-кенгуру, подвійний капюшон.", "Hoodie Basic", nil, nil},
	{"HOD-003", "hoodies", "Худі «Basic»", "Urban Fox", "green", "all", "unisex", []string{"casual"}, 1199,
		"Щільний футер 320 г/м² з начосом, кишеня-кенгуру, подвійний капюшон.", "Hoodie Basic", nil, nil},
	{"HOD-004", "hoodies", "Худі оверсайз «Drop»", "Urban Fox", "beige", "all", "unisex", []string{"street"}, 1499,
		"Вільний крій зі спущеним плечем, щільність 360 г/м².", "Hoodie Drop", nil, nil},
	{"HOD-005", "hoodies", "Худі на блискавці «Zip»", "Nord Line", "navy", "demi", "unisex", []string{"casual", "sport"}, 1399,
		"Худі на металевій блискавці з бічними кишенями.", "Zip Hoodie", nil, nil},
	{"HOD-006", "hoodies", "Світшот «College»", "Stepove", "red", "all", "unisex", []string{"casual", "classic"}, 999,
		"Світшот з круглим вирізом і нашивкою в університетському стилі.", "Sweatshirt", nil, nil},
	{"HOD-007", "hoodies", "Світшот «Вишиванка»", "Stepove", "white", "all", "unisex", []string{"casual"}, 1299,
		"Бавовняний світшот з вишитим орнаментом на грудях.", "Vyshyvanka", nil, nil},
	{"HOD-008", "hoodies", "Флісова кофта «Fleece»", "Carpathia", "khaki", "demi", "unisex", []string{"outdoor"}, 1099,
		"Теплий фліс 280 г/м², комір-стійка, швидко сохне.", "Fleece", nil, nil},
	{"HOD-009", "hoodies", "Спортивне худі «Run»", "Nord Line", "blue", "all", "unisex", []string{"sport"}, 1299,
		"Легке худі з тканини, що відводить вологу. Для бігу й залу.", "Hoodie Run", nil, nil},

	// --- T-shirts ------------------------------------------------------------
	{"TSH-001", "tshirts", "Футболка «Basic»", "Urban Fox", "white", "summer", "unisex", []string{"casual"}, 399,
		"Бавовна 100%, 180 г/м², класичний крій.", "Tee Basic", nil, nil},
	{"TSH-002", "tshirts", "Футболка «Basic»", "Urban Fox", "black", "summer", "unisex", []string{"casual"}, 399,
		"Бавовна 100%, 180 г/м², класичний крій.", "Tee Basic", nil, nil},
	{"TSH-003", "tshirts", "Футболка «Basic»", "Urban Fox", "grey", "summer", "unisex", []string{"casual"}, 399,
		"Бавовна 100%, 180 г/м², класичний крій.", "Tee Basic", nil, nil},
	{"TSH-004", "tshirts", "Футболка оверсайз «Heavy»", "Urban Fox", "beige", "summer", "unisex", []string{"street"}, 599,
		"Щільна бавовна 240 г/м², вільний крій, спущене плече.", "Tee Heavy", nil, nil},
	{"TSH-005", "tshirts", "Футболка «Kyiv»", "Stepove", "navy", "summer", "unisex", []string{"street"}, 549,
		"Футболка з принтом-мапою Києва на спині.", "Tee Kyiv", nil, nil},
	{"TSH-006", "tshirts", "Поло «Club»", "Stepove", "green", "summer", "men", []string{"classic"}, 799,
		"Поло з піке, комір на трьох ґудзиках.", "Polo Club", nil, nil},
	{"TSH-007", "tshirts", "Спортивна футболка «Dry»", "Nord Line", "red", "summer", "unisex", []string{"sport"}, 499,
		"Легкий поліестер з відведенням вологи, пласкі шви.", "Tee Dry", nil, nil},
	{"TSH-008", "tshirts", "Лонгслів «Layer»", "Urban Fox", "brown", "demi", "unisex", []string{"casual"}, 649,
		"Бавовняний лонгслів для багатошарових образів.", "Longsleeve", nil, nil},
	{"TSH-009", "tshirts", "Футболка «Вишиванка»", "Stepove", "white", "summer", "unisex", []string{"casual"}, 699,
		"Бавовняна футболка з вишитим орнаментом на комірі.", "Tee Vyshyvanka", nil, nil},

	// --- Jeans ---------------------------------------------------------------
	{"JNS-001", "jeans", "Джинси «Straight»", "Kyiv Denim", "blue", "all", "unisex", []string{"casual", "classic"}, 1699,
		"Прямий крій, середня посадка, бавовна з 1% еластану.", "Jeans Straight", nil, nil},
	{"JNS-002", "jeans", "Джинси «Straight»", "Kyiv Denim", "black", "all", "unisex", []string{"casual", "classic"}, 1699,
		"Прямий крій, середня посадка, бавовна з 1% еластану.", "Jeans Straight", nil, nil},
	{"JNS-003", "jeans", "Джинси «Slim»", "Kyiv Denim", "blue", "all", "men", []string{"casual"}, 1599,
		"Звужений крій, стрейч-деним.", "Jeans Slim", nil, nil},
	{"JNS-004", "jeans", "Джинси «Slim»", "Kyiv Denim", "grey", "all", "men", []string{"casual"}, 1599,
		"Звужений крій, стрейч-деним.", "Jeans Slim", nil, nil},
	{"JNS-005", "jeans", "Джинси «Wide»", "Kyiv Denim", "blue", "all", "unisex", []string{"street"}, 1899,
		"Широкі джинси з високою посадкою, жорсткий деним.", "Jeans Wide", nil, nil},
	{"JNS-006", "jeans", "Джинси «Mom»", "Kyiv Denim", "blue", "all", "women", []string{"casual"}, 1599,
		"Висока посадка, звуження донизу, вільні в стегнах.", "Jeans Mom", nil, []string{"36"}},
	{"JNS-007", "jeans", "Утеплені джинси «Warm»", "Kyiv Denim", "blue", "winter", "men", []string{"casual", "outdoor"}, 1999,
		"Джинси на флісовій підкладці для морозів.", "Jeans Warm", nil, []string{"28"}},
	{"JNS-008", "jeans", "Карго «Utility»", "Urban Fox", "khaki", "all", "unisex", []string{"street"}, 1499,
		"Штани карго з об'ємними кишенями, бавовняний твіл.", "Cargo", nil, nil},

	// --- Shoes ---------------------------------------------------------------
	{"SHO-001", "shoes", "Кросівки «Runner»", "Nord Line", "white", "all", "unisex", []string{"sport", "casual"}, 2499,
		"Легкі кросівки з амортизуючою підошвою і сітчастим верхом.", "Runner", nil, nil},
	{"SHO-002", "shoes", "Кросівки «Runner»", "Nord Line", "black", "all", "unisex", []string{"sport", "casual"}, 2499,
		"Легкі кросівки з амортизуючою підошвою і сітчастим верхом.", "Runner", nil, nil},
	{"SHO-003", "shoes", "Кеди «Canvas»", "Urban Fox", "white", "summer", "unisex", []string{"casual", "street"}, 1299,
		"Класичні кеди з парусини на гумовій підошві.", "Canvas", nil, nil},
	{"SHO-004", "shoes", "Черевики «Trek»", "Carpathia", "brown", "winter", "men", []string{"outdoor"}, 3299,
		"Утеплені шкіряні черевики з нековзною протекторною підошвою.", "Boots Trek", nil, []string{"37", "38"}},
	{"SHO-005", "shoes", "Челсі «Chelsea»", "Stepove", "black", "demi", "unisex", []string{"classic"}, 2899,
		"Шкіряні челсі з еластичними вставками.", "Chelsea", nil, nil},
	{"SHO-006", "shoes", "Зимові кросівки «Frost»", "Nord Line", "grey", "winter", "unisex", []string{"sport", "outdoor"}, 2799,
		"Кросівки з мембраною і теплою підкладкою.", "Sneakers Frost", nil, nil},
	{"SHO-007", "shoes", "Сліпони «Easy»", "Urban Fox", "blue", "summer", "unisex", []string{"casual"}, 999,
		"Сліпони без шнурків з дихаючого текстилю.", "Slip-on", nil, nil},
	{"SHO-008", "shoes", "Лофери «Office»", "Stepove", "brown", "all", "unisex", []string{"classic"}, 2299,
		"Шкіряні лофери для офісу та урочистих подій.", "Loafers", nil, nil},
	{"SHO-009", "shoes", "Кросівки «Retro»", "Urban Fox", "beige", "all", "unisex", []string{"street", "casual"}, 1999,
		"Кросівки в стилі 80-х із замшевими вставками.", "Retro", nil, nil},

	// --- Accessories ---------------------------------------------------------
	{"ACC-001", "accessories", "Шапка «Beanie»", "Nord Line", "black", "winter", "unisex", []string{"casual"}, 399,
		"В'язана шапка з вовною мериноса, з відворотом.", "Beanie", nil, nil},
	{"ACC-002", "accessories", "Шапка «Beanie»", "Nord Line", "red", "winter", "unisex", []string{"casual"}, 399,
		"В'язана шапка з вовною мериноса, з відворотом.", "Beanie", nil, nil},
	{"ACC-003", "accessories", "Шарф «Wool»", "Stepove", "grey", "winter", "unisex", []string{"classic"}, 699,
		"Вовняний шарф 180×30 см.", "Scarf", nil, nil},
	{"ACC-004", "accessories", "Кепка «Cap»", "Urban Fox", "navy", "summer", "unisex", []string{"street", "sport"}, 449,
		"Бавовняна кепка з регульованою застібкою.", "Cap", nil, nil},
	{"ACC-005", "accessories", "Рукавички «Touch»", "Carpathia", "black", "winter", "unisex", []string{"outdoor"}, 349,
		"Флісові рукавички із сенсорними кінчиками пальців.", "Gloves", nil, nil},
	{"ACC-006", "accessories", "Рюкзак «City»", "Urban Fox", "black", "all", "unisex", []string{"casual", "street"}, 1499,
		"Міський рюкзак на 22 л з відділенням для ноутбука 15\".", "Backpack", nil, nil},
	// Sold out in every size: shows how the catalogue treats a dead end.
	{"ACC-007", "accessories", "Ремінь «Classic»", "Stepove", "brown", "all", "unisex", []string{"classic"}, 599,
		"Шкіряний ремінь з латунною пряжкою.", "Belt", map[string]int{"ONE": 0}, nil},
}

// colorHex paints each placeholder photo in the product's colour, so the demo
// catalogue is at least honest about what colour a thing is.
var colorHex = map[string]string{
	"black": "222222", "white": "f2f2f2", "grey": "9e9e9e", "beige": "d9c3a0",
	"brown": "7b5233", "blue": "3f6fb5", "navy": "1f2f55", "green": "4c8c4a",
	"khaki": "8a8358", "olive": "6b7034", "red": "b33a3a",
}
