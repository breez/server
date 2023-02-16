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
	"google.golang.org/grpc/metadata"
)

func main() {

	//don't check for errors, we are not forcing env file.
	godotenv.Load("sample-clients/.env")

	token := os.Args[1]

	conn, err := GetClientConnection()

	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := breez.NewChannelOpenerClient(conn)
	ctx, cancel := context.WithTimeout(
		metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token),

		1*time.Second,
	)
	defer cancel()
	r, err := c.LSPList(ctx, &breez.LSPListRequest{Pubkey: "testpubkey"})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	for id, info := range r.Lsps {
		fmt.Printf("%s: %#v\n", id, info)
	}
}
