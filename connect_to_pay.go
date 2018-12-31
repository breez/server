package main

import (
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

const (
	payeeJoinedMsgTitle = "Connect To Pay"
	payeeJoinedMsgBody  = "%v is waiting for you to complete a payment you've previously shared. Open to continue with the payment."
	payerJoinedMsgBody  = "%v is waiting for you to join a payment session. Open to continue with receiving this payment."
	payerJoinedMsgTitle = "Receive Payment"
	ctpSessionTTL       = 3600 * 24 // one day
)

var (
	notifyMessages = map[string]map[string]string{
		"payer": map[string]string{"body": payerJoinedMsgBody, "title": payerJoinedMsgTitle},
		"payee": map[string]string{"body": payeeJoinedMsgBody, "title": payeeJoinedMsgTitle},
	}
)

//joinSession is used by both payer/payee to join a CTP session.
//If the sessionID parameter is given then this function looks for an existing session.
//If the sessionID parameter is not given then this function creates a new session.
//Every session that is created is removed automatically after "ctpSessionTTL" in seconds.
func joinSession(existingSessionID, partyToken, partyName string, payer bool) (string, int64, error) {
	partyType := "payer"
	otherParty := "payee"
	if !payer {
		partyType = "payee"
		otherParty = "payer"
	}
	sessionID := existingSessionID

	fmt.Printf("joinSession partyType=%v, partyToken=%v otherParty=%v, sessionID=%v", partyType, partyToken, otherParty, sessionID)
	//if we didn't get session id we are asked to create a new session.
	if sessionID == "" {
		sessionID = uuid.New().String() //generte
	}
	redisSessionKey := fmt.Sprintf("ctp-session-%v", sessionID)

	//if got session id we are asked to join an existing session.
	//We are going to validate that the session exists and not expired.
	if existingSessionID != "" {
		sessionExists, err := keyExists(redisSessionKey)
		if err != nil {
			fmt.Printf("Error in JoinSession: %v", err)
			return "", 0, err
		}
		if !sessionExists {
			fmt.Printf("Error in JoinSession: session doesn't exist or expired")
			return "", 0, fmt.Errorf("Session %v does not exist or expired", sessionID)
		}
	}

	partyTokenKey := fmt.Sprintf("ctp-token-%v", partyType)
	fmt.Printf("partyTokenKey: %v", partyTokenKey)

	err := updateKeyFields(redisSessionKey, map[string]string{
		partyTokenKey: partyToken,
		partyName:     partyName,
	})
	if err != nil {
		return "", 0, err
	}

	//if we just created a new session, put expiration on it
	//so it will be removed automaticaly
	if existingSessionID == "" {
		setKeyExpiration(redisSessionKey, ctpSessionTTL)
	}

	//notify other party about the new user joined the session
	fields, err := getKeyFields(redisSessionKey)
	if err != nil {
		fmt.Printf("Error in JoinSession: %v", err)
		return "", 0, err
	}
	otherPartyTokenKey := fmt.Sprintf("ctp-token-%v", otherParty)
	otherPartyToken := fields[otherPartyTokenKey]
	fmt.Printf("otherPartyToken = %v", otherPartyToken)
	if otherPartyToken != "" {
		go notifyOtherParty(sessionID, partyType, partyName, otherPartyToken)
	}
	ttl, err := getKeyExpiration(redisSessionKey)
	if err != nil {
		return "", 0, err
	}

	expiry := time.Now().Add(time.Second * time.Duration(ttl))
	return sessionID, expiry.Unix(), nil
}

func terminateSession(sessionID string) error {
	return deleteKey(fmt.Sprintf("ctp-session-%v", sessionID))
}

func notifyOtherParty(sessionID, joinedPartyType, joinedPartyName, sendToToken string) {
	data := map[string]string{
		"title": notifyMessages[joinedPartyType]["title"],
		"body":  fmt.Sprintf(notifyMessages[joinedPartyType]["body"], joinedPartyName),
		"msg":   fmt.Sprintf("{\"CTPSessionID\": \"%v\"}", sessionID),
	}

	_, err := notifyDataMessage(data, []string{sendToToken})
	if err != nil {
		log.Println("Error in send:", err)
	}
}
