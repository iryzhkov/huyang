package main

import "os"

func sink() { println("reached") }
func guard(n int) {
	if n == -2 {
		panic("fixture panic")
	}
	if n < 0 {
		return
	}
	sink()
}
func main() {
	n := 1
	if len(os.Args) > 1 {
		n = -1
	}
	if len(os.Args) > 2 {
		n = -2
	}
	guard(n)
}
