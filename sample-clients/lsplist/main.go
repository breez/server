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
	c := breez.NewChannelOpenerClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.LSPList(ctx, &breez.LSPListRequest{Pubkey: "testpubkey"})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	for id, info := range r.Lsps {
		fmt.Printf("%s: %#v\n", id, info)
	}
}
