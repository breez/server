module github.com/breez/server

require (
	cloud.google.com/go v0.26.0
	contrib.go.opencensus.io/exporter/stackdriver v0.6.0 // indirect
	firebase.google.com/go v3.7.0+incompatible
	github.com/SparkPost/gosparkpost v0.0.0-20180607155248-1190f471ed9d
	github.com/breez/lspd v0.0.0-20190722134223-a4ab8bf8fa84
	github.com/btcsuite/btcd v0.0.0-20190629003639-c26ffa870fd8
	github.com/btcsuite/btcutil v0.0.0-20190425235716-9e5f4b9a998d
	github.com/btcsuite/go-flags v0.0.0-20150116065318-6c288d648c1c // indirect
	github.com/codahale/chacha20 v0.0.0-20151107025005-ec07b4f69a3f // indirect
	github.com/codahale/chacha20poly1305 v0.0.0-20151127064032-f8a5c4830182 // indirect
	github.com/golang/protobuf v1.3.2
	github.com/gomodule/redigo v2.0.1-0.20180627144507-2cd21d9966bf+incompatible
	github.com/google/martian v2.1.0+incompatible // indirect
	github.com/google/uuid v1.1.0
	github.com/googleapis/gax-go v2.0.0+incompatible // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.0.0
	github.com/howeyc/gopass v0.0.0-20170109162249-bf9dde6d0d2c // indirect
	github.com/joho/godotenv v1.2.0
	github.com/kylelemons/godebug v0.0.0-20170820004349-d65d576e9348 // indirect
	github.com/lightningnetwork/lnd v0.7.0-beta
	github.com/mojocn/base64Captcha v0.0.0-20190801020520-752b1cd608b2
	github.com/pkg/errors v0.8.0
	go.opencensus.io v0.15.0 // indirect
	golang.org/x/net v0.0.0-20190628185345-da137c7871d7
	golang.org/x/oauth2 v0.0.0-20180821212333-d2e6202438be
	golang.org/x/sync v0.0.0-20190423024810-112230192c58
	golang.org/x/text v0.3.1-0.20180807135948-17ff2d5776d2
	google.golang.org/api v0.0.0-20180818000503-e21acd801f91
	google.golang.org/grpc v1.22.0
)

replace github.com/lightningnetwork/lnd v0.7.0-beta => github.com/breez/lnd v0.7.0-beta.0.20190704080717-2fa048807c28

go 1.13
