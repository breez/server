package main

import (
	"os"

	fcm "github.com/NaySoftware/go-fcm"
)

type notificationConfig struct {
	title        string
	body         string
	icon         string
	sound        string
	highPriority bool
	data         map[string]string
}

func defaultNotificationConfig() *notificationConfig {
	return &notificationConfig{
		icon:  "breez_notify",
		sound: "default",
		data: map[string]string{
			"click_action": "FLUTTER_NOTIFICATION_CLICK",
			"collapseKey":  "breez",
		},
	}
}

func notify(conf *notificationConfig, tokens []string) error {
	notificationClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
	status, err := notificationClient.NewFcmRegIdsMsg(tokens, conf.data).
		SetPriority(fcm.Priority_HIGH).
		SetNotificationPayload(&fcm.NotificationPayload{
			Title: conf.title,
			Body:  conf.body,
			Icon:  conf.icon,
			Sound: conf.sound}).
		Send()

	status.PrintResults()
	return err
}
