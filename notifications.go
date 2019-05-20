package main

import (
	"firebase.google.com/go/messaging"
	"context"
	"log"
	"sync"
	"strings"

	firebase "firebase.google.com/go"	
)
var firebaseMu sync.Mutex

func firebaseApp() (*firebase.App, error) {
	firebaseMu.Lock()	
	defer firebaseMu.Unlock()
	return firebase.NewApp(context.Background(), nil)
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

	status, err := client.Send(context.Background(), &messaging.Message{
		Token: token,		
		Data: data,
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

	log.Printf("Data Notification Status = %v, Error = %v", status, err)
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
		Data: data,
		Android: &messaging.AndroidConfig{
			CollapseKey: "breez",
			Priority: "high",			
		},
		APNS: &messaging.APNSConfig{
			Headers: map[string]string{
				"apns-priority": "5",				
			},
			Payload: &messaging.APNSPayload{
				Aps: &messaging.Aps{
					Alert: &messaging.ApsAlert{
						Title: title,
						Body: body,
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
	return strings.Contains(err.Error(), "not a valid FCM registration token")
}
