package main

import (
	"context"
	"log"
	"strconv"

	"github.com/breez/server/breez"
)

const (
	ratesKey = "RATES:BTC"
)

func (s *server) Rates(ctx context.Context, in *breez.RatesRequest) (*breez.RatesReply, error) {

	ratesMap, err := getKeyFields(ratesKey)
	if err != nil {
		log.Printf("Error in getKeyFields(\"%s\"): %v", ratesKey, err)
		return nil, err
	}

	rates := make([]*breez.Rate, 0, len(ratesMap))
	for c, v := range ratesMap {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			rates = append(rates, &breez.Rate{Coin: c, Value: f})
		}

	}

	return &breez.RatesReply{Rates: rates}, nil
}
