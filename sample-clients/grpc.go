package samples

import (
	"crypto/x509"
	"log"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

/*
GetClientConnection client connection based on env configuration.
*/
func GetClientConnection() (*grpc.ClientConn, error) {
	var dialOptions []grpc.DialOption
	if os.Getenv("NO_TLS") == "1" {
		dialOptions = append(dialOptions, grpc.WithInsecure())
	} else {
		systemCertPool, err := x509.SystemCertPool()
		if err != nil {
			log.Fatal("Error getting SystemCertPool: %v", err)
		}
		creds := credentials.NewClientTLSFromCert(systemCertPool, "")

		dialOptions = append(dialOptions, grpc.WithTransportCredentials(creds))
	}
	serverAddress := os.Getenv("SERVER_ADDRESS")
	return grpc.Dial(serverAddress, dialOptions...)
}
