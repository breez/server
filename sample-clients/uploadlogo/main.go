package main

import (
	"context"
	"io/ioutil"
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
	posClient := breez.NewPosClient(conn)

	// Contact the server and print out its response.
	if len(os.Args) < 2 {
		os.Exit(1)
	}
	logoPath := os.Args[1]
	data, err := ioutil.ReadFile(logoPath)
	if err != nil {
		log.Fatalf("Can not read image file %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	r, err := posClient.UploadLogo(ctx, &breez.UploadFileRequest{Content: data})
	if err != nil {
		log.Println("Failed Upload Logo", err)
	}
	log.Println("result:", r)
}
