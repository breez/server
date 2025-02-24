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
	lspList(c, ctx)
	fmt.Println(" ---- ---- ----")
	lspFullList(c, ctx)
}

func lspList(c breez.ChannelOpenerClient, ctx context.Context) {
	r, err := c.LSPList(ctx, &breez.LSPListRequest{Pubkey: "testpubkey"})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	for id, info := range r.Lsps {
		fmt.Printf("%s: %#v\n", id, info)
		if info.OpeningFeeParamsMenu == nil {
			fmt.Println("NO OpeningFeeParamsMenu - PROBLEM")
		} else {
			fmt.Println("OpeningFeeParamsMenu")
			for _, m := range info.OpeningFeeParamsMenu {
				fmt.Printf("%#v\n", m)
				fmt.Printf("m.MinMsat: %v\n", m.MinMsat)
				fmt.Printf("m.Proportional: %v\n", m.Proportional)
				fmt.Printf("m.ValidUntil: %v\n", m.ValidUntil)
			}
		}
		fmt.Println()
	}
}

func lspFullList(c breez.ChannelOpenerClient, ctx context.Context) {
	r, err := c.LSPFullList(ctx, &breez.LSPFullListRequest{Pubkey: "testpubkey"})
	if err != nil {
		log.Fatalf("err: %v", err)
	}
	for i, info := range r.Lsps {
		fmt.Printf("%s: %#v\n", i, info)
		if info.OpeningFeeParamsMenu == nil {
			fmt.Println("NO OpeningFeeParamsMenu - PROBLEM - inactive")
		} else {
			fmt.Println("OpeningFeeParamsMenu")
			for _, m := range info.OpeningFeeParamsMenu {
				fmt.Printf("%#v\n", m)
				fmt.Printf("m.MinMsat: %v\n", m.MinMsat)
				fmt.Printf("m.Proportional: %v\n", m.Proportional)
				fmt.Printf("m.ValidUntil: %v\n", m.ValidUntil)
			}
		}
		fmt.Println()
	}
}
