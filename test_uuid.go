package main
import "fmt"
func main() {
    b := []byte{0xab, 0xcd}
    fmt.Printf("%x\n", b)
    fmt.Printf("%X\n", b)
}
