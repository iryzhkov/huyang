package main

func main() {
	value := 1
	println(value)
	done := make(chan struct{})
	go func() {
		value = 2
		close(done)
	}()
	<-done
	value = 3
	println(value)
}
