package seed

// The demo catalogue of an auto parts shop.
//
// The vehicles are real models, but the parts are not: brands, part numbers
// and fitments are invented for the demo and are not a compatibility
// reference. Made-up brands also let the bot be shown publicly without
// borrowing anyone's trademark.

type category struct {
	slug, title string
	// imageColor tints the placeholder photos of the category.
	imageColor string
}

var categories = []category{
	{"brakes", "Гальмівна система", "b33a3a"},
	{"filters", "Фільтри", "3f6fb5"},
	{"oils", "Масла та рідини", "c9a227"},
	{"suspension", "Підвіска", "4c8c4a"},
	{"engine", "Двигун: ремені, свічки", "6b5b95"},
	{"electrical", "Електрика", "2f7f86"},
}

type vehicle struct {
	code             string
	make, model      string
	yearFrom, yearTo int // yearTo 0: still produced
	engine, fuel     string
	// Service data. Illustrative, like the rest of the catalogue: close to
	// the real figures, not a workshop reference.
	oilViscosity string
	oilVolume    float64
}

var vehicles = []vehicle{
	{"octavia-a7-14tsi", "Skoda", "Octavia A7", 2013, 2020, "1.4 TSI", "petrol", "5W-30", 3.6},
	{"octavia-a7-20tdi", "Skoda", "Octavia A7", 2013, 2020, "2.0 TDI", "diesel", "5W-30", 4.6},
	{"golf-7-16tdi", "Volkswagen", "Golf VII", 2012, 2019, "1.6 TDI", "diesel", "5W-30", 4.3},
	{"passat-b8-20tdi", "Volkswagen", "Passat B8", 2014, 2022, "2.0 TDI", "diesel", "5W-30", 4.6},
	{"camry-xv70-25", "Toyota", "Camry XV70", 2017, 2023, "2.5", "petrol", "0W-20", 4.4},
	{"corolla-e210-16", "Toyota", "Corolla E210", 2019, 0, "1.6", "petrol", "0W-20", 4.2},
	{"logan-2-15dci", "Renault", "Logan II", 2012, 2020, "1.5 dCi", "diesel", "10W-40", 4.5},
	{"logan-2-16", "Renault", "Logan II", 2012, 2020, "1.6 8V", "petrol", "10W-40", 3.3},
	{"tucson-tl-20", "Hyundai", "Tucson TL", 2015, 2020, "2.0 MPI", "petrol", "0W-20", 4.0},
	{"sportage-ql-20", "Kia", "Sportage QL", 2015, 2021, "2.0 MPI", "petrol", "0W-20", 4.0},
	{"focus-3-16", "Ford", "Focus III", 2011, 2018, "1.6 Ti-VCT", "petrol", "5W-30", 4.1},
	{"lanos-15", "Daewoo", "Lanos", 1997, 2009, "1.5", "petrol", "10W-40", 3.8},
}

// Fitment groups: vehicles that share a platform share many parts.
var (
	vag        = []string{"octavia-a7-14tsi", "octavia-a7-20tdi", "golf-7-16tdi", "passat-b8-20tdi"}
	vagDiesel  = []string{"octavia-a7-20tdi", "golf-7-16tdi", "passat-b8-20tdi"}
	toyota     = []string{"camry-xv70-25", "corolla-e210-16"}
	logan      = []string{"logan-2-15dci", "logan-2-16"}
	hyundaiKia = []string{"tucson-tl-20", "sportage-ql-20"}
	focus      = []string{"focus-3-16"}
	lanos      = []string{"lanos-15"}
)

// one builds a fitment list of named vehicles.
func one(codes ...string) []string { return codes }

type spec = [2]string

type product struct {
	sku, category string
	partType      string
	name, brand   string
	price         int64 // whole hryvnias
	fits          []string
	universal     bool
	oem           []string
	specs         []spec
	description   string
	imageLabel    string // English text drawn on the placeholder photo
	// stock overrides the generated stock when set; stockSoldOut means 0.
	stock int
}

// stockSoldOut pins a product at zero, which the zero value of stock cannot.
const stockSoldOut = -1

var products = []product{
	// --- Brakes --------------------------------------------------------------
	{sku: "BRK-001", category: "brakes", name: "Колодки гальмівні передні", brand: "AutoLine", price: 1450,
		fits: vag, oem: []string{"5Q0 698 151", "1K0 698 151"},
		specs:       []spec{{"Вісь", "передня"}, {"Товщина", "20.3 мм"}, {"Датчик зносу", "ні"}},
		description: "Органічні колодки з низьким пилоутворенням для щоденної їзди містом і трасою.",
		imageLabel:  "Brake pads",
		// A single unit on purpose: stage 3 races two buyers for it.
		stock: 1},
	{sku: "BRK-002", category: "brakes", name: "Колодки гальмівні передні керамічні", brand: "Stal Pro", price: 2290,
		fits: vag, oem: []string{"5Q0 698 151"},
		specs:       []spec{{"Вісь", "передня"}, {"Товщина", "20.3 мм"}, {"Склад", "кераміка"}},
		description: "Керамічні колодки: тихі, майже без пилу на дисках, стабільні при нагріванні.",
		imageLabel:  "Ceramic pads"},
	{sku: "BRK-003", category: "brakes", name: "Колодки гальмівні передні", brand: "AutoLine", price: 1690,
		fits: toyota, oem: []string{"04465-33480"},
		specs:       []spec{{"Вісь", "передня"}, {"Товщина", "17.5 мм"}},
		description: "Колодки з противибраційними пластинами в комплекті.", imageLabel: "Brake pads"},
	{sku: "BRK-004", category: "brakes", name: "Колодки гальмівні передні", brand: "AutoLine", price: 890,
		fits: logan, oem: []string{"41 06 084 81R"},
		specs:       []spec{{"Вісь", "передня"}, {"Товщина", "18 мм"}},
		description: "Бюджетні колодки з ресурсом до 40 тис. км.", imageLabel: "Brake pads"},
	{sku: "BRK-005", category: "brakes", name: "Колодки гальмівні передні", brand: "Pivden Parts", price: 1290,
		fits: hyundaiKia, oem: []string{"58101-D7A00"},
		specs:       []spec{{"Вісь", "передня"}, {"Датчик зносу", "так"}},
		description: "Колодки з акустичним датчиком зносу.", imageLabel: "Brake pads"},
	{sku: "BRK-006", category: "brakes", name: "Диск гальмівний передній вентильований", brand: "Stal Pro", price: 1890,
		fits: vag, oem: []string{"5Q0 615 301 F"},
		specs:       []spec{{"Вісь", "передня"}, {"Діаметр", "312 мм"}, {"Ціна за", "1 шт."}},
		description: "Вентильований диск з антикорозійним покриттям. Міняти парою на осі.",
		imageLabel:  "Brake disc"},
	{sku: "BRK-007", category: "brakes", name: "Диск гальмівний передній", brand: "AutoLine", price: 1150,
		fits: logan, oem: []string{"40 20 600 10R"},
		specs:       []spec{{"Вісь", "передня"}, {"Діаметр", "259 мм"}, {"Ціна за", "1 шт."}},
		description: "Суцільний диск. Міняти парою на осі.", imageLabel: "Brake disc"},
	{sku: "BRK-008", category: "brakes", name: "Колодки гальмівні передні", brand: "AutoLine", price: 1050,
		fits: focus, oem: []string{"1830418"},
		specs:       []spec{{"Вісь", "передня"}},
		description: "Колодки з фаскою для швидкого притирання.", imageLabel: "Brake pads"},
	// Sold out: shows how the catalogue treats a dead end.
	{sku: "BRK-009", category: "brakes", name: "Колодки гальмівні передні", brand: "Pivden Parts", price: 450,
		fits: lanos, oem: []string{"96273708"},
		specs:       []spec{{"Вісь", "передня"}},
		description: "Колодки для Lanos і Sens.", imageLabel: "Brake pads", stock: stockSoldOut},

	// --- Filters -------------------------------------------------------------
	{sku: "FLT-001", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "FiltroMax", price: 290,
		fits: one("octavia-a7-14tsi"), oem: []string{"04E 115 561 H"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Фільтр з антидренажним клапаном.", imageLabel: "Oil filter"},
	// The same OEM number from another brand: an analogue of FLT-001.
	{sku: "FLT-002", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "AutoLine", price: 240,
		fits: one("octavia-a7-14tsi"), oem: []string{"04E 115 561 H"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Бюджетний аналог оригінального фільтра.", imageLabel: "Oil filter"},
	{sku: "FLT-003", partType: "oil_filter", category: "filters", name: "Фільтр масляний (картридж)", brand: "FiltroMax", price: 340,
		fits: vagDiesel, oem: []string{"03N 115 562"},
		specs:       []spec{{"Тип", "картридж"}},
		description: "Фільтруючий елемент з ущільнювальними кільцями.", imageLabel: "Oil filter"},
	{sku: "FLT-004", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "FiltroMax", price: 260,
		fits: toyota, oem: []string{"90915-10003"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Фільтр для бензинових двигунів Toyota.", imageLabel: "Oil filter"},
	{sku: "FLT-005", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "FiltroMax", price: 230,
		fits: logan, oem: []string{"82 00 768 913"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Підходить до бензинових і дизельних Logan.", imageLabel: "Oil filter"},
	{sku: "FLT-006", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "FiltroMax", price: 220,
		fits: hyundaiKia, oem: []string{"26300-35505"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Фільтр для двигунів 2.0 MPI.", imageLabel: "Oil filter"},
	{sku: "FLT-007", partType: "air_filter", category: "filters", name: "Фільтр повітряний", brand: "FiltroMax", price: 480,
		fits: vag, oem: []string{"5Q0 129 620 B"},
		specs:       []spec{{"Форма", "прямокутний"}},
		description: "Багатошаровий фільтруючий матеріал.", imageLabel: "Air filter"},
	{sku: "FLT-008", partType: "air_filter", category: "filters", name: "Фільтр повітряний", brand: "FiltroMax", price: 420,
		fits: hyundaiKia, oem: []string{"28113-D3300"},
		specs:       []spec{{"Форма", "прямокутний"}},
		description: "Фільтр повітряний двигуна.", imageLabel: "Air filter"},
	{sku: "FLT-009", partType: "cabin_filter", category: "filters", name: "Фільтр салону вугільний", brand: "FiltroMax", price: 520,
		fits: vag, oem: []string{"5Q0 819 653"},
		specs:       []spec{{"Тип", "вугільний"}},
		description: "Затримує пил, пилок і запахи з вулиці.", imageLabel: "Cabin filter"},
	{sku: "FLT-010", partType: "oil_filter", category: "filters", name: "Фільтр масляний", brand: "Pivden Parts", price: 120,
		fits: lanos, oem: []string{"25183779"},
		specs:       []spec{{"Тип", "накручуваний"}},
		description: "Фільтр для Lanos 1.5.", imageLabel: "Oil filter"},

	// --- Oils and fluids: universal, the specs decide ------------------------
	{sku: "OIL-001", partType: "engine_oil", category: "oils", name: "Моторна олива 5W-30 синтетична, 4 л", brand: "LubriTech", price: 1290,
		universal:   true,
		specs:       []spec{{"В'язкість", "5W-30"}, {"Об'єм", "4 л"}, {"Допуски", "VW 504.00 / 507.00"}},
		description: "Синтетика з низьким вмістом золи для двигунів з сажовим фільтром.",
		imageLabel:  "Oil 5W-30"},
	{sku: "OIL-002", partType: "engine_oil", category: "oils", name: "Моторна олива 5W-30 синтетична, 1 л", brand: "LubriTech", price: 380,
		universal:   true,
		specs:       []spec{{"В'язкість", "5W-30"}, {"Об'єм", "1 л"}, {"Допуски", "VW 504.00 / 507.00"}},
		description: "Та сама олива в літровій каністрі — для доливання.", imageLabel: "Oil 5W-30"},
	{sku: "OIL-003", partType: "engine_oil", category: "oils", name: "Моторна олива 0W-20 синтетична, 4 л", brand: "LubriTech", price: 1490,
		universal:   true,
		specs:       []spec{{"В'язкість", "0W-20"}, {"Об'єм", "4 л"}, {"Стандарт", "API SP, ILSAC GF-6"}},
		description: "Енергозберігаюча олива для сучасних японських і корейських двигунів.",
		imageLabel:  "Oil 0W-20"},
	{sku: "OIL-004", partType: "engine_oil", category: "oils", name: "Моторна олива 10W-40 напівсинтетична, 4 л", brand: "LubriTech", price: 890,
		universal:   true,
		specs:       []spec{{"В'язкість", "10W-40"}, {"Об'єм", "4 л"}, {"Стандарт", "API SL/CF"}},
		description: "Для двигунів з пробігом і старших авто.", imageLabel: "Oil 10W-40"},
	{sku: "OIL-005", category: "oils", name: "Антифриз G12+ червоний, 5 л", brand: "LubriTech", price: 590,
		universal:   true,
		specs:       []spec{{"Тип", "G12+"}, {"Об'єм", "5 л"}, {"Готовий", "так, до −36 °C"}},
		description: "Готовий до використання, змішується з G12 і G11.", imageLabel: "Coolant G12+"},
	{sku: "OIL-006", category: "oils", name: "Гальмівна рідина DOT 4, 1 л", brand: "LubriTech", price: 260,
		universal:   true,
		specs:       []spec{{"Клас", "DOT 4"}, {"Об'єм", "1 л"}},
		description: "Для систем з ABS і ESP. Міняти раз на два роки.", imageLabel: "DOT 4"},
	{sku: "OIL-007", category: "oils", name: "Омивач скла зимовий −20 °C, 4 л", brand: "LubriTech", price: 190,
		universal:   true,
		specs:       []spec{{"Температура", "до −20 °C"}, {"Об'єм", "4 л"}},
		description: "Без різкого запаху, не залишає розводів.", imageLabel: "Washer fluid"},
	{sku: "OIL-008", category: "oils", name: "Трансмісійна олива 75W-90, 1 л", brand: "LubriTech", price: 420,
		universal:   true,
		specs:       []spec{{"В'язкість", "75W-90"}, {"Об'єм", "1 л"}, {"Стандарт", "API GL-4/GL-5"}},
		description: "Для механічних коробок передач і редукторів.", imageLabel: "Gear oil"},

	// --- Suspension ----------------------------------------------------------
	{sku: "SUS-001", category: "suspension", name: "Амортизатор передній газовий", brand: "Stal Pro", price: 2390,
		fits: vag, oem: []string{"5Q0 413 031"},
		specs:       []spec{{"Вісь", "передня"}, {"Тип", "газомасляний"}, {"Ціна за", "1 шт."}},
		description: "Амортизатор зі збільшеним ресурсом для наших доріг. Міняти парою.",
		imageLabel:  "Shock absorber"},
	{sku: "SUS-002", category: "suspension", name: "Амортизатор передній газовий", brand: "AutoLine", price: 1790,
		fits: vag, oem: []string{"5Q0 413 031"},
		specs:       []spec{{"Вісь", "передня"}, {"Тип", "газомасляний"}, {"Ціна за", "1 шт."}},
		description: "Доступний аналог з тим самим номером.", imageLabel: "Shock absorber"},
	{sku: "SUS-003", category: "suspension", name: "Стійка стабілізатора передня", brand: "AutoLine", price: 390,
		fits: vag, oem: []string{"1K0 411 315 R"},
		specs:       []spec{{"Вісь", "передня"}, {"Сторона", "ліва/права"}},
		description: "Тяга стабілізатора з посиленими пильовиками.", imageLabel: "Stabilizer link"},
	{sku: "SUS-004", category: "suspension", name: "Амортизатор передній правий", brand: "Stal Pro", price: 2890,
		fits: one("camry-xv70-25"), oem: []string{"48510-80789"},
		specs:       []spec{{"Вісь", "передня"}, {"Сторона", "права"}},
		description: "Стійка в зборі для правої сторони.", imageLabel: "Strut"},
	{sku: "SUS-005", category: "suspension", name: "Кульова опора", brand: "Pivden Parts", price: 520,
		fits: logan, oem: []string{"40 16 073 11R"},
		specs:       []spec{{"Сторона", "ліва/права"}},
		description: "Опора з кріпленням, ресурс до 60 тис. км.", imageLabel: "Ball joint"},
	{sku: "SUS-006", category: "suspension", name: "Стійка стабілізатора передня", brand: "Pivden Parts", price: 450,
		fits: hyundaiKia, oem: []string{"54830-D3000"},
		specs:       []spec{{"Вісь", "передня"}},
		description: "Тяга стабілізатора.", imageLabel: "Stabilizer link"},
	{sku: "SUS-007", category: "suspension", name: "Сайлентблок переднього важеля", brand: "AutoLine", price: 340,
		fits: focus, oem: []string{"1500243"},
		specs:       []spec{{"Положення", "задній"}},
		description: "Поліуретан-гумовий сайлентблок.", imageLabel: "Bushing"},
	{sku: "SUS-008", category: "suspension", name: "Амортизатор передній масляний", brand: "Pivden Parts", price: 990,
		fits: lanos, oem: []string{"96226990"},
		specs:       []spec{{"Вісь", "передня"}, {"Тип", "масляний"}},
		description: "Амортизатор для Lanos і Sens.", imageLabel: "Shock absorber"},

	// --- Engine --------------------------------------------------------------
	{sku: "ENG-001", category: "engine", name: "Комплект ГРМ (ремінь + ролики)", brand: "Stal Pro", price: 3890,
		fits: vagDiesel, oem: []string{"04L 198 119 A"},
		specs:       []spec{{"Склад", "ремінь, натяжний і обвідний ролики"}, {"Інтервал заміни", "120 тис. км"}},
		description: "Повний комплект для заміни ременя ГРМ на дизелях 1.6 і 2.0 TDI.",
		imageLabel:  "Timing kit"},
	{sku: "ENG-002", partType: "spark_plug", category: "engine", name: "Свічка запалювання", brand: "Iskra", price: 260,
		fits: one("octavia-a7-14tsi"), oem: []string{"04E 905 612"},
		specs:       []spec{{"Тип", "нікелева"}, {"Ціна за", "1 шт."}, {"Потрібно", "4 шт."}},
		description: "Свічка для турбованих двигунів 1.4 TSI.", imageLabel: "Spark plug"},
	{sku: "ENG-003", partType: "spark_plug", category: "engine", name: "Свічка запалювання іридієва", brand: "Iskra", price: 520,
		fits: toyota, oem: []string{"90919-01289"},
		specs:       []spec{{"Тип", "іридієва"}, {"Ціна за", "1 шт."}, {"Потрібно", "4 шт."}},
		description: "Ресурс до 100 тис. км.", imageLabel: "Iridium plug"},
	{sku: "ENG-004", category: "engine", name: "Ремінь генератора поліклиновий", brand: "AutoLine", price: 380,
		fits: logan, oem: []string{"11 72 078 15R"},
		specs:       []spec{{"Профіль", "6PK1070"}},
		description: "Ремінь приводу навісного обладнання.", imageLabel: "Belt"},
	{sku: "ENG-005", category: "engine", name: "Комплект ГРМ", brand: "Stal Pro", price: 2190,
		fits: one("logan-2-16"), oem: []string{"13 0C 174 80R"},
		specs:       []spec{{"Склад", "ремінь, натяжний ролик"}, {"Інтервал заміни", "60 тис. км"}},
		description: "Комплект ГРМ для 1.6 8V.", imageLabel: "Timing kit"},
	{sku: "ENG-006", partType: "spark_plug", category: "engine", name: "Свічка запалювання", brand: "Iskra", price: 230,
		fits: hyundaiKia, oem: []string{"18849-11051"},
		specs:       []spec{{"Тип", "нікелева"}, {"Ціна за", "1 шт."}, {"Потрібно", "4 шт."}},
		description: "Свічка для 2.0 MPI.", imageLabel: "Spark plug"},
	{sku: "ENG-007", category: "engine", name: "Котушка запалювання", brand: "Iskra", price: 1290,
		fits: focus, oem: []string{"1857081"},
		specs:       []spec{{"Тип", "модуль на 4 циліндри"}},
		description: "Котушка-модуль, ставиться без доопрацювань.", imageLabel: "Ignition coil"},
	{sku: "ENG-008", category: "engine", name: "Ремінь ГРМ", brand: "Pivden Parts", price: 420,
		fits: lanos, oem: []string{"96352914"},
		specs:       []spec{{"Зубців", "111"}},
		description: "Ремінь ГРМ для Lanos 1.5.", imageLabel: "Timing belt"},

	// --- Electrical ----------------------------------------------------------
	{sku: "ELC-001", category: "electrical", name: "Акумулятор 60 А·год 540 А", brand: "Iskra", price: 3190,
		universal:   true,
		specs:       []spec{{"Ємність", "60 А·год"}, {"Пусковий струм", "540 А"}, {"Полярність", "зворотна (+ праворуч)"}},
		description: "Кальцієвий акумулятор для більшості легкових авто. Перевірте розміри посадкового місця.",
		imageLabel:  "Battery 60Ah"},
	{sku: "ELC-002", category: "electrical", name: "Акумулятор 74 А·год 680 А", brand: "Iskra", price: 4290,
		universal:   true,
		specs:       []spec{{"Ємність", "74 А·год"}, {"Пусковий струм", "680 А"}, {"Полярність", "зворотна (+ праворуч)"}},
		description: "Для авто з більшим двигуном або дизелем.", imageLabel: "Battery 74Ah"},
	{sku: "ELC-003", category: "electrical", name: "Лампа H7 12V 55W, 2 шт.", brand: "Iskra", price: 290,
		universal:   true,
		specs:       []spec{{"Цоколь", "H7"}, {"Потужність", "55 Вт"}, {"У комплекті", "2 шт."}},
		description: "Галогенова лампа ближнього світла.", imageLabel: "Bulb H7"},
	{sku: "ELC-004", category: "electrical", name: "Лампа H4 12V 60/55W", brand: "Iskra", price: 210,
		universal:   true,
		specs:       []spec{{"Цоколь", "H4"}, {"Потужність", "60/55 Вт"}},
		description: "Двониткова лампа ближнього й дальнього світла.", imageLabel: "Bulb H4"},
	{sku: "ELC-005", category: "electrical", name: "Лампа LED W5W, 2 шт.", brand: "Iskra", price: 180,
		universal:   true,
		specs:       []spec{{"Цоколь", "W5W"}, {"У комплекті", "2 шт."}},
		description: "Світлодіодні габарити, біле світло.", imageLabel: "LED W5W"},
	{sku: "ELC-006", category: "electrical", name: "Щітки склоочисника 600/450 мм, комплект", brand: "AutoLine", price: 690,
		fits:        vag,
		specs:       []spec{{"Довжина", "600 + 450 мм"}, {"Тип", "безкаркасні"}},
		description: "Безкаркасні щітки з кріпленням під оригінал.", imageLabel: "Wipers"},
	{sku: "ELC-007", category: "electrical", name: "Датчик ABS передній", brand: "Pivden Parts", price: 890,
		fits: hyundaiKia, oem: []string{"95670-F2000"},
		specs:       []spec{{"Вісь", "передня"}},
		description: "Датчик швидкості колеса.", imageLabel: "ABS sensor"},
}
