package calculator_test

import (
	calculator "testLogCapture"
	"testing"
)

func TestAdd(t *testing.T) {
	got := calculator.Add(2, 3)
	if got != 5 {
		t.Errorf("Add(2,3) = %d, want 5", got)
	}
}

func TestSub(t *testing.T) {
	got := calculator.Sub(10, 4)
	if got != 6 {
		t.Errorf("Sub(10,4) = %d, want 6", got)
	}
}

func TestMul(t *testing.T) {
	got := calculator.Mul(3, 7)
	if got != 21 {
		t.Errorf("Mul(3,7) = %d, want 21", got)
	}
}

func TestDiv(t *testing.T) {
	got, err := calculator.Div(10, 2)
	if err != nil {
		t.Fatalf("Div(10,2) unexpected error: %v", err)
	}
	if got != 5 {
		t.Errorf("Div(10,2) = %d, want 5", got)
	}
}

func TestDivByZero(t *testing.T) {
	_, err := calculator.Div(5, 0)
	if err == nil {
		t.Error("Div(5,0) expected error, got nil")
	}
}

func TestSqrt(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{4, 2},
		{9, 3},
		{16, 4},
		{25, 5},
	}

	for _, tt := range tests {
		got, err := calculator.Sqrt(tt.input)
		if err != nil {
			t.Errorf("Sqrt(%d) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Sqrt(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestSqrtNegative(t *testing.T) {
	_, err := calculator.Sqrt(-1)
	if err == nil {
		t.Error("Sqrt(-1) expected error, got nil")
	}
}
