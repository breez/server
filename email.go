package main

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log"
	"os"
	"strconv"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/breez/server/breez"
)

const (
	charset = "UTF-8"
)

func addresses(a string) (addr []*string) {
	json.Unmarshal([]byte(a), &addr)
	return
}

func sendEmail(to, cc, from, content, subject string) error {

	sess, err := session.NewSession(&aws.Config{})
	if err != nil {
		log.Printf("Error in session.NewSession: %v", err)
		return err
	}
	svc := ses.New(sess)

	input := &ses.SendEmailInput{
		Destination: &ses.Destination{
			CcAddresses: addresses(cc),
			ToAddresses: addresses(to),
		},
		Message: &ses.Message{
			Body: &ses.Body{
				Html: &ses.Content{
					Charset: aws.String(charset),
					Data:    aws.String(content),
				},
			},
			Subject: &ses.Content{
				Charset: aws.String(charset),
				Data:    aws.String(subject),
			},
		},
		Source: aws.String(from),
	}
	// Attempt to send the email.
	result, err := svc.SendEmail(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ses.ErrCodeMessageRejected:
				log.Println(ses.ErrCodeMessageRejected, aerr.Error())
			case ses.ErrCodeMailFromDomainNotVerifiedException:
				log.Println(ses.ErrCodeMailFromDomainNotVerifiedException, aerr.Error())
			case ses.ErrCodeConfigurationSetDoesNotExistException:
				log.Println(ses.ErrCodeConfigurationSetDoesNotExistException, aerr.Error())
			default:
				log.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			log.Println(err.Error())
		}
		return err
	}

	log.Printf("Email sent with result:\n%v", result)

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
		os.Getenv("CARD_NOTIFICATION_TO"),
		os.Getenv("CARD_NOTIFICATION_CC"),
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

func sendOpenChannelNotification(provider, nid, txid string, index uint32) error {

	channelID := txid + ":" + strconv.FormatUint(uint64(index), 10)

	var html bytes.Buffer

	tpl := `
	<div>Provider: {{ .Provider }}</div>
	<div>NodeId: {{ .NodeID }}</div>
	<div>Channel: {{ .ChannelID }}</div>
	`
	t, err := template.New("OpenChannelEmail").Parse(tpl)
	if err != nil {
		return err
	}

	if err := t.Execute(&html, map[string]string{"Provider": provider, "NodeID": nid, "ChannelID": channelID}); err != nil {
		return err
	}

	err = sendEmail(
		os.Getenv("OPENCHANNEL_NOTIFICATION_TO"),
		os.Getenv("OPENCHANNEL_NOTIFICATION_CC"),
		os.Getenv("OPENCHANNEL_NOTIFICATION_FROM"),
		html.String(),
		"Open Channel",
	)
	if err != nil {
		log.Printf("Error sending open channel email: %v", err)
		return err
	}

	return nil
}

func sendPaymentFailureNotification(in *breez.ReportPaymentFailureRequest) error {
	var html bytes.Buffer

	tpl := `
	<div>SdkVersion: {{ .SdkVersion }}</div>
	<div>SdkGitHash: {{ .SdkGitHash }}</div>
	<div>NodeId: {{ .NodeId }}</div>
	<div>LspId: {{ .LspId }}</div>
	<div>Timestamp: {{ .Timestamp }}</div>
	<div>Comment/error: {{ .Comment }}</div>
	<div>Report:</div>
	<div>{{ .Report }}</div>
	`
	t, err := template.New("PaymentFailureEmail").Parse(tpl)
	if err != nil {
		log.Printf("Error parsing HTML template: %v", err)
		return err
	}

	if err := t.Execute(&html, in); err != nil {
		log.Printf("Error applying data to template: %v", err)
		return err
	}

	err = sendEmail(
		os.Getenv("PAYMENT_FAILURE_NOTIFICATION_TO"),
		os.Getenv("PAYMENT_FAILURE_NOTIFICATION_CC"),
		os.Getenv("PAYMENT_FAILURE_NOTIFICATION_FROM"),
		html.String(),
		"Payment Failure",
	)
	if err != nil {
		log.Printf("Error sending payment failure email: %v", err)
		return err
	}

	return nil
}
