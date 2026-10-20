package reports

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// Excel writes the report as a workbook: the totals, the orders one by
// one, and what sold, by product and by category.
func Excel(r *Report) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, fmt.Errorf("reports: style: %w", err)
	}
	money, err := f.NewStyle(&excelize.Style{NumFmt: 4}) // #,##0.00
	if err != nil {
		return nil, fmt.Errorf("reports: style: %w", err)
	}

	sheet := func(name string, header []any, rows [][]any, widths []float64, moneyCols ...string) error {
		if _, err := f.NewSheet(name); err != nil {
			return err
		}
		if err := f.SetSheetRow(name, "A1", &header); err != nil {
			return err
		}
		last, _ := excelize.CoordinatesToCellName(len(header), 1)
		if err := f.SetCellStyle(name, "A1", last, bold); err != nil {
			return err
		}
		for i, row := range rows {
			cell, _ := excelize.CoordinatesToCellName(1, i+2)
			if err := f.SetSheetRow(name, cell, &row); err != nil {
				return err
			}
		}
		for i, w := range widths {
			col, _ := excelize.ColumnNumberToName(i + 1)
			if err := f.SetColWidth(name, col, col, w); err != nil {
				return err
			}
		}
		for _, col := range moneyCols {
			if err := f.SetCellStyle(name, col+"2", fmt.Sprintf("%s%d", col, len(rows)+1), money); err != nil {
				return err
			}
		}
		return f.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	}

	hrn := func(m int64) float64 { return float64(m) / 100 }
	totals := [][]any{
		{"Період", r.Period.Label},
		{"Оплачених замовлень", r.Orders},
		{"Виручка, грн", hrn(int64(r.Revenue))},
		{"Середній чек, грн", hrn(int64(r.AverageCheck()))},
		{"Самовивіз", r.Pickup},
		{"Нова Пошта", r.Delivery},
		{"Під замовлення (замовити в постачальника), грн", hrn(int64(r.OnOrder))},
		{"Повернення, замовлень", r.Refunds},
		{"Повернення, грн", hrn(int64(r.RefundSum))},
		{"Неоплачених замовлень", r.Unpaid},
		{"Запитів під замовлення", r.Requests.Made},
		{"  з них знайдено і названо ціну", r.Requests.Quoted},
		{"  оплачено", r.Requests.Paid},
		{"  не знайдено", r.Requests.NotFound},
		{"  покупець відмовився", r.Requests.Declined},
		{"  ціна прострочена", r.Requests.Expired},
		{"  ще чекають менеджера", r.Requests.Waiting},
	}
	if err := sheet("Підсумок", []any{"Показник", "Значення"}, totals, []float64{48, 28}); err != nil {
		return nil, fmt.Errorf("reports: totals sheet: %w", err)
	}

	sales := make([][]any, len(r.Sales))
	for i, s := range r.Sales {
		how := "Нова Пошта"
		if s.Pickup {
			how = "Самовивіз"
		}
		sales[i] = []any{s.ID, s.PaidAt.Format("02.01.2006 15:04"), s.Customer, s.Phone, how, s.Delivery,
			statusLabel(string(s.Status)), s.Items, hrn(int64(s.Total))}
	}
	if err := sheet("Замовлення", []any{"№", "Оплачено", "Покупець", "Телефон", "Отримання", "Адреса", "Статус",
		"Товарів", "Сума, грн"}, sales, []float64{8, 17, 24, 16, 13, 36, 14, 9, 13}, "I"); err != nil {
		return nil, fmt.Errorf("reports: orders sheet: %w", err)
	}

	lines := func(name string, ls []Line, withBrand bool) error {
		rows := make([][]any, len(ls))
		for i, l := range ls {
			if withBrand {
				rows[i] = []any{l.Name, l.Brand, l.Qty, hrn(int64(l.Sum))}
			} else {
				rows[i] = []any{l.Name, l.Qty, hrn(int64(l.Sum))}
			}
		}
		if withBrand {
			return sheet(name, []any{"Товар", "Бренд", "Кількість", "Сума, грн"}, rows, []float64{52, 16, 11, 13}, "D")
		}
		return sheet(name, []any{"Категорія", "Кількість", "Сума, грн"}, rows, []float64{48, 11, 13}, "C")
	}
	if err := lines("Товари", r.Products, true); err != nil {
		return nil, fmt.Errorf("reports: products sheet: %w", err)
	}
	if err := lines("Категорії", r.Categories, false); err != nil {
		return nil, fmt.Errorf("reports: categories sheet: %w", err)
	}

	sources := make([][]any, len(r.Sources))
	for i, s := range r.Sources {
		mark := s.Mark
		if mark == "" {
			mark = "без мітки"
		}
		sources[i] = []any{mark, s.NewBuyers, s.Orders, hrn(int64(s.Revenue))}
	}
	if err := sheet("Джерела", []any{"Мітка посилання", "Нових покупців", "Замовлень", "Сума, грн"},
		sources, []float64{24, 16, 12, 13}, "D"); err != nil {
		return nil, fmt.Errorf("reports: sources sheet: %w", err)
	}

	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, fmt.Errorf("reports: %w", err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("reports: write: %w", err)
	}
	return buf.Bytes(), nil
}

// statusLabel names an order's status for the managers.
func statusLabel(s string) string {
	switch s {
	case "paid":
		return "Оплачено"
	case "confirmed":
		return "Підтверджено"
	case "shipped":
		return "Відправлено/видано"
	}
	return s
}
