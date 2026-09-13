package main

import (
	"fmt"
	"os"
)

func dispatch(credits int) string {
	if credits <= 0 {
		return "rejected"
	}
	return deliver(credits)
}

func deliver(credits int) string {
	return fmt.Sprintf("sent:%d", credits)
}

func run(credits int) string {
	result := make(chan string)
	go func() { result <- dispatch(credits) }()
	return <-result
}

func main() {
	credits := 1
	if len(os.Args) > 1 && os.Args[1] == "fail" {
		credits = 0
	}
	result := run(credits)
	fmt.Println(result)
	if result == "rejected" {
		os.Exit(1)
	}
}
