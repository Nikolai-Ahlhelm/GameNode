//go:build ignore

package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	fmt.Println("READY")
	fmt.Fprintln(os.Stderr, "HELPER_STDERR_READY")
	s := bufio.NewScanner(os.Stdin)
	for s.Scan() {
		if s.Text() == "exit" {
			return
		}
		fmt.Println("ECHO:" + s.Text())
	}
}
