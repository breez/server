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
	c := breez.NewCTPClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.JoinCTPSession(ctx, &breez.JoinCTPSessionRequest{
		PartyType:         breez.JoinCTPSessionRequest_PAYEE,
		NotificationToken: "eC7btM-abk8:APA91bEWbrScpF6R5udRUK7Rl2DlJzCexdEFqkfVO9WBG87EDt1uV1kSlXokFAI7aHbCvvsWfiuVCGJCYQzv8bc9_AHUrnX_XF1WX742EbNumBjF6nJ-4jDmi5v8_uuuTPBQNMxijXIL",
		SessionID:         "3b2355fa-cd69-4a4b-a0b7-ff0a3c405d99",
		PartyName:         "Payee",
	})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	fmt.Printf("Session id: %s\n", r.SessionID)
}
