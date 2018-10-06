package main

import (
	"context"
	"fmt"
	"log"
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
	c := breez.NewCardOrdererClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.Order(ctx, &breez.OrderRequest{
		FullName: "John Doe",
		Address:  "1 Downing Street",
		City:     "Paris",
		State:    "NA",
		Zip:      "12345",
	})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("Breez order response: %s\n", r)
}
