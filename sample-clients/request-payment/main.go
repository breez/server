package main

import (
	"context"
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
	c := breez.NewInvoicerClient(conn)

	// Contact the server and print out its response.
	if len(os.Args) < 3 {
		os.Exit(1)
	}
	breezID := os.Args[1]
	invoice := os.Args[2]
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.SendInvoice(ctx, &breez.PaymentRequest{BreezID: breezID, Invoice: invoice})
	log.Printf("Result: %#v error: %v", r, err)
}
