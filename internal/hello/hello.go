package hello

import "fmt"

const Version = "0.1.0"

func Print() {
	fmt.Printf("smtpify started (version %s)\n", Version)
}
