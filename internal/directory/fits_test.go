package directory

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
)

func TestFitLines(t *testing.T) {
	// BREMBO S 59 513, as the live API lists it.
	vs := []autodb.ArticleVehicle{
		{ModID: 22591, Make: "CHEVROLET", Model: "EPICA (KL1_)", Years: "01.2005 - 12.2006", Engine: "2.0"},
		{ModID: 19604, Make: "CHEVROLET", Model: "EPICA (KL1_)", Years: "06.2006 - ", Engine: "2.5"},
		{ModID: 18536, Make: "CHEVROLET", Model: "LACETTI (J200)", Years: "03.2005 - ", Engine: "1.4 16V"},
		{ModID: 18537, Make: "CHEVROLET", Model: "LACETTI (J200)", Years: "03.2005 - ", Engine: "1.6"},
		{ModID: 1, Make: "DAEWOO", Model: "NUBIRA седан (J100)", Years: "05.1997 - 07.1999", Engine: "1.6"},
		{ModID: 2, Make: "DAEWOO", Model: "NUBIRA седан (J100)", Years: "05.1997 - 07.1999", Engine: "1.6"},
	}
	assert.Equal(t, []string{
		"Chevrolet Epica (KL1_) 2.0, 2.5 (2005–)",
		"Chevrolet Lacetti (J200) 1.4 16V, 1.6 (2005–)",
		"Daewoo Nubira Седан (J100) 1.6 (1997–1999)",
	}, FitLines(vs))
}
