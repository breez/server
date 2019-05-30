package main

import (
	"log"
	"time"
)

const (
	syncSetName  = "sync_notifications_set"
	syncInterval = time.Duration(time.Minute * 30)
	syncJobName  = "chainSync"
)

// registerSyncNotification registeres a device for a periodic sync notification.
// the client will get a data message every "syncInterval" and will be responsible
// to execute a sync.
func registerSyncNotification(deviceToken string) error {
	_, err := pushWithScore(
		syncSetName, deviceToken, time.Now().Add(syncInterval).Unix())
	return err
}

// deliverSyncNotifications executes the main loop of runnig over existing registration
// and sending sync messags on time.
func deliverSyncNotifications() {
	for {
		deviceToken, score, err := popMinScore(syncSetName)
		if err != nil {
			log.Println("failed to pop next sync notification ", err)
			continue
		}
		fireTime := time.Unix(int64(score), 0)
		<-time.After(fireTime.Sub(time.Now()))
		go func() {
			unreg, err := sendClientSyncMessage(deviceToken)
			if err != nil {
				log.Println("error in sending sync message:", err)
			}

			//if this token is still valid, register for the next sync time.
			if !unreg {
				if err = registerSyncNotification(deviceToken); err != nil {
					log.Println("failed to re-regiseter sync notification for token: ", deviceToken)
				}
			}
		}()
	}
}

// sendClientSyncMessage is the function that actualy sends the sync message
// to the client. It also returns a value indicates if this token needs to be
// unregistered.
func sendClientSyncMessage(sendToToken string) (bool, error) {
	data := map[string]string{
		"_job": syncJobName,
	}

	err := notifyDataMessage(data, sendToToken)
	if err != nil {
		if isUnregisteredError(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}
