package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/breez/server/breez"
	. "github.com/breez/server/sample-clients"

	"github.com/joho/godotenv"
)

func main() {
	if len(os.Args) < 3 {
		log.Fatalln("Needs at least a clientid and one address")
	}
	clientID := os.Args[1]
	addresses := os.Args[2:]

	//don't check for errors, we are not forcing env file.
	godotenv.Load("sample-clients/.env")

	conn, err := GetClientConnection()

	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := breez.NewMempoolNotifierClient(conn)

	// Contact the server and print out its response.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := c.MempoolRegister(ctx, &breez.MempoolRegisterRequest{ClientID: clientID, Addresses: addresses})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("MempoolRegister: %#v\n", r)
}
