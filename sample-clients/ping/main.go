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
	c := breez.NewInformationClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.Ping(ctx, &breez.PingRequest{})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("Breez server version: %s\n", r.Version)

	v, err := c.BreezAppVersions(ctx, &breez.BreezAppVersionsRequest{})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("Breez app versions: %#v\n", v.Version)
}
