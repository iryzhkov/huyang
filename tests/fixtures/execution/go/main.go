package main

import (
	"fmt"
	"os"
)

type Worker interface{ Work(int) int }
type incrementer struct{}

func (incrementer) Work(n int) int { return n + 1 }

func leaf(n int) int                      { return n * 2 }
func indirect(f func(int) int, n int) int { return f(n) }
func recursive(n int) int {
	if n <= 0 {
		return 0
	}
	return 1 + recursive(n-1)
}
func guarded(w Worker, n int) int {
	if n < 0 {
		return -1
	}
	if n == 0 {
		return leaf(n)
	}
	return indirect(leaf, w.Work(n))
}
func concurrent(n int) int {
	ch := make(chan int)
	go func() { ch <- guarded(incrementer{}, n) }()
	return <-ch
}
func main() {
	n := 1
	if len(os.Args) > 1 && os.Args[1] == "fail" {
		n = -1
	}
	value := concurrent(n)
	fmt.Println(value, recursive(2))
	if value < 0 {
		os.Exit(1)
	}
}
