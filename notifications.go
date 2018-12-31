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

func notifyDataMessage(data map[string]string, tokens []string) (*fcm.FcmResponseStatus, error) {
	notificationClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
	if data["click_action"] == "" {
		data["click_action"] = "FLUTTER_NOTIFICATION_CLICK"
	}
	if data["collapseKey"] == "" {
		data["collapseKey"] = "breez"
	}
	status, err := notificationClient.NewFcmRegIdsMsg(tokens, data).
		SetPriority(fcm.Priority_HIGH).
		Send()

	status.PrintResults()
	return status, err
}
