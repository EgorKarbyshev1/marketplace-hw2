package service

import (
	"encoding/json"
	"marketplace/internal/generated"
	"testing"
)

func TestDiscountMoneyRounding(t *testing.T) {
	for _, test := range []struct {
		total    int64
		kind     generated.DiscountType
		value    string
		expected int64
	}{
		{10025, "PERCENTAGE", "20", 2005},
		{3, "PERCENTAGE", "50", 2},
		{1, "PERCENTAGE", "70", 0},
		{101, "PERCENTAGE", "70", 70},
		{10000, "PERCENTAGE", "70.01", 0},
		{3, "FIXED_AMOUNT", "20", 3},
	} {
		actual := calculateDiscount(test.total, generated.PromoCodeResponse{DiscountType: test.kind, DiscountValue: json.Number(test.value)})
		if actual != test.expected {
			t.Errorf("total=%d type=%s value=%s: got %d want %d", test.total, test.kind, test.value, actual, test.expected)
		}
	}
}
