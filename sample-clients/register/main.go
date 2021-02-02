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

const (
	defaultID = "test_id"
)

func main() {

	//don't check for errors, we are not forcing env file.
	godotenv.Load("sample-clients/.env")

	conn, err := GetClientConnection()

	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := breez.NewInvoicerClient(conn)

	// Contact the server and print out its response.
	id := defaultID
	if len(os.Args) > 1 {
		id = os.Args[1]
	}
	nodeID := ""
	if len(os.Args) > 2 {
		nodeID = os.Args[2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.RegisterDevice(ctx, &breez.RegisterRequest{DeviceID: id, LightningID: nodeID})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("breez id: %s\n", r.BreezID)
}
