package main

func target() {}
func source() {
	if true {
		target()
	} else {
		target()
	}
}
func main() { source() }
