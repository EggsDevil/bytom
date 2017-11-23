package main

import (
	"fmt"
	"log"
	"github.com/bytom/exp/ivy/contract"
	"encoding/hex"
	"os"
)

func main() {
	var pub string

	if(len(os.Args) >= 2) {
		pub = os.Args[1]
	} else {
		fmt.Println("command args: [arg]")
		os.Exit(1)
	}

	publickey, _ := hex.DecodeString(pub)
	datastr, err := contract.PayToLockWithPublicKey(publickey)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("compiled result: %s\n", hex.EncodeToString(datastr))

	_, err = contract.ParsePayToLockWithPublicKey(datastr)
	if err != nil {
		log.Fatal(err)
	}

}
