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

	//don't check for errors, we are not forcing env file.
	godotenv.Load("sample-clients/.env")

	conn, err := GetClientConnection()

	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()

	c := breez.NewPosClient(conn)

	// Contact the server and print out its response.
	id := ""
	if len(os.Args) > 1 {
		id = os.Args[1]
	} else {
		log.Fatalf("Need an id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.FundChannel(ctx, &breez.FundRequest{DeviceID: "", LightningID: id, Amount: 1234567})
	fmt.Printf("Result %#v %v\n", r, err)
}
