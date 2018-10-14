# server
## Build
```
go build .
```
## Run
First export the following environment variables:
```
LISTEN_ADDRESS="<host>:<port>"

FCM_KEY=<Your FCM key to send notifications>

LND_ADDRESS="<host>:<port>"
LND_CERT="-----BEGIN CERTIFICATE-----\\n<certificate content replacing each eol by \\n>\\n-----END CERTIFICATE-----\\n"
LND_MACAROON_HEX="<content of the macaroon file in hexadecimal>"
NETWORK=simnet #or testnet or mainnet

GOOGLE_CLOUD_SERVICE_FILE=<full path of the json file>
GOOGLE_CLOUD_IMAGES_BUCKET_NAME=<name>.appspot.com

REDIS_URL="127.0.0.1:6379"
REDIS_DB="6"
```

Then run:
```
./server
```
