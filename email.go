package main

import (
	"bytes"
	"html/template"
	"log"
	"os"
	"strconv"

	sp "github.com/SparkPost/gosparkpost"
	"github.com/breez/server/breez"
)

func sendEmail(email, name, from, content, subject string) error {
	apiKey := os.Getenv("SPARKPOST_API_KEY")
	cfg := &sp.Config{
		BaseUrl:    "https://api.sparkpost.com",
		ApiKey:     apiKey,
		ApiVersion: 1,
	}
	var client sp.Client
	err := client.Init(cfg)
	if err != nil {
		log.Printf("SparkPost client init failed: %s", err)
		return err
	}

	// Create a Transmission using an inline Recipient List
	// and inline email Content.
	tx := &sp.Transmission{
		Recipients: []sp.Recipient{{Address: sp.Address{Email: email, Name: name}}},
		Content: sp.Content{
			HTML:    content,
			From:    from,
			Subject: subject,
		},
	}
	id, _, err := client.Send(tx)
	if err != nil {
		log.Printf("Error sending email: %v", err)
		return err
	}

	// The second value returned from Send
	// has more info about the HTTP response, in case
	// you'd like to see more than the Transmission id.
	log.Printf("Transmission sent with id [%s]\n", id)
	return nil
}

func sendCardOrderNotification(in *breez.OrderRequest) error {

	var html bytes.Buffer

	tpl := `
	<div>FullName: {{ .FullName }}</div>
	<div>Address: {{ .Address }}</div>
	<div>City: {{ .City }}</div>
	<div>State: {{ .State }}</div>
	<div>Zip: {{ .Zip }}</div>
	<div>Country: {{ .Country }}</div>
	<div>Email: {{ .Email }}</div>
	`
	t, err := template.New("OrderCardEmail").Parse(tpl)
	if err != nil {
		return err
	}

	if err := t.Execute(&html, in); err != nil {
		return err
	}

	err = sendEmail(
		os.Getenv("CARD_NOTIFICATION_EMAIL"),
		os.Getenv("CARD_NOTIFICATION_NAME"),
		os.Getenv("CARD_NOTIFICATION_FROM"),
		html.String(),
		"Card Order",
	)
	if err != nil {
		log.Printf("Error sending order card email: %v", err)
		return err
	}

	return nil
}

func sendOpenChannelNotification(nid, txid string, index uint32) error {

	channelID := txid + ":" + strconv.FormatUint(uint64(index), 10)

	var html bytes.Buffer

	tpl := `
	<div>NodeId: {{ .NodeID }}</div>
	<div>Channel: {{ .ChannelID }}</div>
	`
	t, err := template.New("OpenChannelEmail").Parse(tpl)
	if err != nil {
		return err
	}

	if err := t.Execute(&html, map[string]string{"NodeID": nid, "ChannelID": channelID}); err != nil {
		return err
	}

	err = sendEmail(
		os.Getenv("CARD_NOTIFICATION_EMAIL"),
		os.Getenv("CARD_NOTIFICATION_NAME"),
		os.Getenv("CARD_NOTIFICATION_FROM"),
		html.String(),
		"Open Channel",
	)
	if err != nil {
		log.Printf("Error sending open channel email: %v", err)
		return err
	}

	return nil
}
