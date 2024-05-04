package main

import (
	"context"
	"log"
	"os"
	"sync"

	"firebase.google.com/go/messaging"

	firebase "firebase.google.com/go"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

var firebaseMu sync.Mutex

func firebaseApp() (*firebase.App, error) {
	firebaseMu.Lock()
	defer firebaseMu.Unlock()
	creds, err := google.CredentialsFromJSON(context.Background(), []byte(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")), "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return nil, err
	}
	return firebase.NewApp(context.Background(), nil, option.WithCredentials(creds))
}

func notifyDataMessage(data map[string]string, token string) error {
	app, err := firebaseApp()
	if err != nil {
		return err
	}

	client, err := app.Messaging(context.Background())
	if err != nil {
		return err
	}

	iosCustomData := make(map[string]interface{})
	for key, value := range data {
		iosCustomData[key] = value
	}

	_, err = client.Send(context.Background(), &messaging.Message{
		Token: token,
		Data:  data,
		Android: &messaging.AndroidConfig{
			Priority: "high",
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority": "10",
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					ContentAvailable: true,
				},
			},
		},
	})

	return err
}

func notifyAlertMessage(title, body string, data map[string]string, token string) error {
	app, err := firebaseApp()
	if err != nil {
		return err
	}

	client, err := app.Messaging(context.Background())
	if err != nil {
		return err
	}

	if data["click_action"] == "" {
		data["click_action"] = "FLUTTER_NOTIFICATION_CLICK"
	}
	data["title"] = title
	data["body"] = body

	iosCustomData := make(map[string]interface{})
	for key, value := range data {
		iosCustomData[key] = value
	}

	status, err := client.Send(context.Background(), &messaging.Message{
		Token: token,
		Data:  data,
		Android: &messaging.AndroidConfig{
			CollapseKey: "breez",
			Priority:    "high",
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority": "5",
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body:  body,
					},
					CustomData: iosCustomData,
				},
			},
		},
	})

	log.Printf("Alert Notification Status = %v, Error = %v", status, err)
	return err
}

func isUnregisteredError(err error) bool {
	return messaging.IsRegistrationTokenNotRegistered(err)
}
