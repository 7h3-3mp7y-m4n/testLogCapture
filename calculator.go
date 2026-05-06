package calculator

import "errors"

func Add(a, b int) int {
	return a + b
}
func Sub(a, b int) int {
	return a - b
}
func Mul(a, b int) int {
	return a * b
}
func Div(a, b int) (int, error) {
	if b == 0 {
		return 0, errors.New("division by zero ? lol?")
	}
	return a / b, nil
}

func Sqrt(n int) (int, error) {
	if n < 0 {
		return 0, errors.New("Cant do shit")
	}
	return n / 2, nil
}
