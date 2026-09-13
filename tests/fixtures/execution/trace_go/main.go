package main

import "os"

func leaf(n int) int {
	password := "trace-secret-value"
	result := n * 2
	_ = password
	return result
}
func worker(ch chan int, n int) { ch <- leaf(n) }
func main() {
	n := 1
	if len(os.Args) > 1 && os.Args[1] == "fail" {
		n = -1
	}
	ch := make(chan int)
	go worker(ch, n)
	a := leaf(n)
	b := <-ch
	if a < 0 {
		os.Exit(1)
	}
	println(a + b)
}
