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

const (
	lspToken = "1WL4gWZLZJ40qkXpeiUzJE7GCo4WhRXZXJbdhuP7GLg="
)

func main() {

	//don't check for errors, we are not forcing env file.
	godotenv.Load("sample-clients/.env")

	conn, err := GetClientConnection()

	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := breez.NewSwapperClient(conn)
	ctx, cancel := context.WithTimeout(
		//metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lspToken),
		context.Background(),
		1*time.Second,
	)
	defer cancel()
	r, err := c.GetReverseRoutingNode(ctx, &breez.GetReverseRoutingNodeRequest{})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("RoutingNode: %x\n", r.NodeId)
}
