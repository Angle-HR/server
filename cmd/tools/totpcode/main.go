// Command totpcode prints the current 6-digit TOTP code for a base32 secret (the e2e scripts use it to play the
// authenticator app).
//
//	go run ./cmd/tools/totpcode JBSWY3DPEHPK3PXP        # code for now
//	go run ./cmd/tools/totpcode -offset 60 SECRET       # code for now+60s (a different time step)
//	echo SECRET | go run ./cmd/tools/totpcode           # secret on stdin
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
)

func main() {
	offset := flag.Int("offset", 0, "seconds to add to the current time")
	flag.Parse()
	secret := strings.TrimSpace(flag.Arg(0))
	if secret == "" {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		secret = strings.TrimSpace(line)
	}
	if secret == "" {
		fmt.Fprintln(os.Stderr, "usage: totpcode [-offset seconds] SECRET")
		os.Exit(2)
	}
	code, err := totp.GenerateCode(secret, time.Now().Add(time.Duration(*offset)*time.Second))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(code)
}
