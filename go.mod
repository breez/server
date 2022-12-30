module github.com/breez/server

go 1.14

require (
	cloud.google.com/go/firestore v1.3.0 // indirect
	cloud.google.com/go/storage v1.27.0
	firebase.google.com/go v3.7.0+incompatible
	github.com/aws/aws-sdk-go v1.33.0
	github.com/breez/boltz v0.0.0-20200114203444-0c01ddb93028
	github.com/breez/lspd v0.0.0-20210616153301-b86a77ab6925
	github.com/btcsuite/btcd v0.21.0-beta.0.20201208033208-6bd4c64a54fa
	github.com/btcsuite/btcutil v1.0.2
	github.com/golang/protobuf v1.5.2
	github.com/gomodule/redigo v2.0.1-0.20180627144507-2cd21d9966bf+incompatible
	github.com/google/uuid v1.3.0
	github.com/grpc-ecosystem/go-grpc-middleware v1.0.0
	github.com/jackc/pgtype v1.4.2
	github.com/jackc/pgx/v4 v4.8.1
	github.com/joho/godotenv v1.2.0
	github.com/lightningnetwork/lnd v0.12.1-beta
	github.com/mojocn/base64Captcha v0.0.0-20190801020520-752b1cd608b2
	github.com/pkg/errors v0.9.1
	go.starlark.net v0.0.0-20200821142938-949cc6f4b097
	golang.org/x/oauth2 v0.0.0-20221014153046-6fdb5e3db783
	golang.org/x/sync v0.1.0
	golang.org/x/text v0.5.0
	google.golang.org/api v0.102.0
	google.golang.org/grpc v1.50.1
	google.golang.org/grpc/examples v0.0.0-20221228232309-12b8fb52a18c // indirect
)

replace github.com/lightningnetwork/lnd => github.com/breez/lnd v0.12.1-beta.rc6.0.20210719131344-b444ae37125d
