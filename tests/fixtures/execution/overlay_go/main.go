package main

import "os"

func sink()                  { println("hit") }
func left()                  { sink() }
func right()                 { sink() }
func invoke(callback func()) { callback() }
func main() {
	if len(os.Args) > 1 {
		left()
	} else {
		right()
	}
	invoke(sink)
}
