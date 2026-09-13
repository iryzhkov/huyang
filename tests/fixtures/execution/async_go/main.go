package main

func target()            {}
func source(ch chan int) { go target(); <-ch; target() }
func main()              {}
