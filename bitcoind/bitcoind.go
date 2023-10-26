package bitcoind

import (
	"fmt"
	"log"
	"os"
	"strconv"

	gbitcoind "github.com/toorop/go-bitcoind"
)

func GetSenderAddresses(destTxs []string) ([]string, error) {
	bitcoindPort, err := strconv.Atoi(os.Getenv("BITCOIND_PORT"))
	if err != nil {
		return nil, fmt.Errorf("no valid port for bitcoind: %v", os.Getenv("BITCOIND_PORT"))
	}
	bc, err := gbitcoind.New(os.Getenv("BITCOIND_HOST"), bitcoindPort, os.Getenv("BITCOIND_USER"), os.Getenv("BITCOIND_PASSWORD"), false)
	if err != nil {
		return nil, fmt.Errorf("cannot create a bitcoind client")
	}
	addrs := []string{}
	for _, txid := range destTxs {
		t, err := bc.GetRawTransaction(txid, true)
		if err != nil {
			log.Fatalf("error: %v", err)
		}
		rawtx, ok := t.(gbitcoind.RawTransaction)
		if !ok {
			continue
		}
		for _, vin := range rawtx.Vin {
			tin, err := bc.GetRawTransaction(vin.Txid, true)
			if err != nil {
				log.Fatalf("error: %v", err)
			}
			rawtin, ok := tin.(gbitcoind.RawTransaction)
			if !ok {
				continue
			}
			spk := rawtin.Vout[vin.Vout].ScriptPubKey
			addrs = append(addrs, spk.Address)
			addrs = append(addrs, spk.Addresses...)
			//fmt.Printf("from addr: %v\n", address)
		}
	}
	return addrs, nil
}
