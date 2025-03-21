package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/breez/server/breez"
	"github.com/breez/server/swapper"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/google/uuid"
	"github.com/jackc/pgtype"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TypeUnknown = iota
	TypeBoltzReverseSwapLockup
)

const (
	StatusUnknown = iota
	StatusUnconfirmed
	StatusNotified
)

type BoltzReverseSwapInfo struct {
	ID                 string `json:"id"`
	TimeoutBlockHeight uint32 `json:"timeout_block_height"`
}

var (
	pgxPool *pgxpool.Pool
)

func pgConnect() error {
	var err error
	pgxPool, err = pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		return fmt.Errorf("pgxpool.New(%v): %w", os.Getenv("DATABASE_URL"), err)
	}
	return nil
}

func insertSubswapPayment(paymentHash, paymentRequest string, lockheight, confirmationheight int32, utxos []string) error {
	commandTag, err := pgxPool.Exec(context.Background(),
		`INSERT INTO swap_payments
          (payment_hash, payment_request, lock_height, confirmation_height, utxos, redeem_confirmed)
          VALUES ($1, $2, $3, $4, $5, false)
          ON CONFLICT DO NOTHING`,
		paymentHash,
		paymentRequest,
		lockheight,
		confirmationheight,
		utxos,
	)
	if err != nil {
		log.Printf("pgxPool.Exec('INSERT INTO swap_payments(%v, %v): %v",
			paymentHash, paymentRequest, err)
		return fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("pgxPool.Exec('INSERT INTO swap_payments(%v, %v)'; RowsAffected(): %v'",
		paymentHash, paymentRequest, commandTag.RowsAffected())
	return nil
}

func getInProgressRedeems(blockheight int32) ([]*swapper.InProgressRedeem, error) {
	ignoreBefore := blockheight - (288 * 100)
	rows, err := pgxPool.Query(context.Background(),
		`SELECT payment_hash
		 ,      payment_preimage
		 ,      lock_height
		 ,      confirmation_height
		 ,      utxos
		 ,      txid
		 FROM swap_payments
		 WHERE redeem_confirmed = false
		 	AND payment_preimage is not null
		 	AND confirmation_height > $1
		 ORDER BY confirmation_height
	`, ignoreBefore)
	if err != nil {
		return nil, fmt.Errorf("failed to query swap_payments: %w", err)
	}
	defer rows.Close()

	var result []*swapper.InProgressRedeem
	for rows.Next() {
		var payment_hash string
		var payment_preimage *string
		var lock_height int32
		var confirmation_height int32
		var utxos []string
		var txid []string
		err = rows.Scan(
			&payment_hash,
			&payment_preimage,
			&lock_height,
			&confirmation_height,
			&utxos,
			&txid,
		)
		if err != nil {
			return nil, fmt.Errorf("rows.Scan() error: %w", err)
		}

		result = append(result, &swapper.InProgressRedeem{
			PaymentHash:        payment_hash,
			Preimage:           payment_preimage,
			LockHeight:         lock_height,
			ConfirmationHeight: confirmation_height,
			Utxos:              utxos,
			RedeemTxids:        txid,
		})
	}

	return result, nil
}

func updateSubswapPreimage(paymentHash, paymentPreimage string) error {
	commandTag, err := pgxPool.Exec(context.Background(),
		`UPDATE swap_payments
         SET
          payment_preimage=$2
         WHERE payment_hash=$1`, paymentHash, paymentPreimage)
	if err != nil {
		log.Printf("updateSubswapPreimage(%v, %v): %v",
			paymentHash, paymentPreimage, err)
		return fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("updateSubswapPreimage(%v, %v)'; RowsAffected(): %v'",
		paymentHash, paymentPreimage, commandTag.RowsAffected())
	return nil
}

func updateSubswapTxid(paymentHash, txid string) error {
	commandTag, err := pgxPool.Exec(context.Background(),
		`UPDATE swap_payments
         SET
          txid=txid||$2
         WHERE payment_hash=$1`, paymentHash, []string{txid})
	if err != nil {
		log.Printf("updateSubswapTxid(%v, %v): %v",
			paymentHash, txid, err)
		return fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("updateSubswapTxid(%v, %v)'; RowsAffected(): %v'",
		paymentHash, txid, commandTag.RowsAffected())
	return nil
}

func setSubswapConfirmed(paymentHash string) error {
	_, err := pgxPool.Exec(context.Background(),
		`UPDATE swap_payments
		 SET redeem_confirmed = true
		 WHERE payment_hash = $1
		`,
		paymentHash,
	)
	return err
}

func insertTxNotification(in *breez.PushTxNotificationRequest) (*uuid.UUID, error) {
	u, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("uuid.NewRandom(): %w", err)
	}
	var additionalInfo []byte
	var txType int32
	switch x := in.Info.(type) {
	case *breez.PushTxNotificationRequest_BoltzReverseSwapLockupTxInfo:
		additionalInfo, _ = json.Marshal(BoltzReverseSwapInfo{
			ID:                 x.BoltzReverseSwapLockupTxInfo.BoltzId,
			TimeoutBlockHeight: x.BoltzReverseSwapLockupTxInfo.TimeoutBlockHeight,
		})
		txType = TypeBoltzReverseSwapLockup
	default:
		txType = TypeUnknown
	}
	commandTag, err := pgxPool.Exec(context.Background(),
		`INSERT INTO tx_notifications
		  (id, tx_type, status, additional_info, title, body, device_id, tx_hash, script, block_height_hint)
		  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		  ON CONFLICT (device_id, script) DO NOTHING`,
		pgtype.UUID{Bytes: u, Status: pgtype.Present},
		txType,
		StatusUnconfirmed,
		additionalInfo,
		in.Title,
		in.Body,
		in.DeviceId,
		in.TxHash,
		in.Script,
		in.BlockHeightHint,
	)
	if err != nil {
		log.Printf("pgxPool.Exec(): %v", err)
		return nil, fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("pgxPool.Exec('INSERT INTO tx_notification()'; RowsAffected(): %v'", commandTag.RowsAffected())
	if commandTag.RowsAffected() == 0 {
		return nil, nil
	}
	return &u, nil
}

func txNotified(u uuid.UUID, txHash chainhash.Hash, tx []byte, blockHeigh uint32, blockHash []byte, txIndex uint32) error {
	commandTag, err := pgxPool.Exec(context.Background(),
		`UPDATE tx_notifications
		 SET status = $2, tx_hash=$3, tx=$4, block_height=$5, block_hash=$6, tx_index=$7
		 WHERE id=$1`,
		u, StatusNotified, txHash.CloneBytes(), tx, blockHeigh, blockHash, txIndex,
	)
	if err != nil {
		log.Printf("pgxPool.Exec(): %v", err)
		return fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("pgxPool.Exec('UPDATE tx_notifications'; RowsAffected(): %v'", commandTag.RowsAffected())
	return nil
}

func boltzReverseSwapToNotify(currentHeight uint32) (pgx.Rows, error) {
	return pgxPool.Query(context.Background(),
		`SELECT id, additional_info, title, body, device_id, tx_hash, script, block_height_hint
		 FROM tx_notifications tn
		 WHERE tn.tx_type=$1 AND tn.status=$2 AND cast(tn.additional_info->>'timeout_block_height' as int)>$3`,
		TypeBoltzReverseSwapLockup, StatusUnconfirmed, currentHeight,
	)
}

func breezAppVersion() (pgx.Rows, error) {
	return pgxPool.Query(context.Background(),
		`SELECT version FROM breez_app_versions`,
	)
}

func breezStatus() (string, error) {
	var statusCode string
	err := pgxPool.QueryRow(context.Background(),
		`SELECT value->>'code' as status
		FROM breez_info
		WHERE "key"='status'
		ORDER by "timestamp" DESC
		LIMIT 1`).Scan(&statusCode)
	return statusCode, err
}

func deviceNode(nodeID []byte, deviceID string) error {
	commandTag, err := pgxPool.Exec(context.Background(),
		`INSERT INTO deviceid_nodeid
		  (nodeid, deviceid, first_registration)
		  VALUES ($1, $2, NOW())
		  ON CONFLICT (nodeid) DO UPDATE SET deviceid=$2`,
		nodeID, deviceID)
	if err != nil {
		log.Printf("pgxPool.Exec(): %v", err)
		return fmt.Errorf("pgxPool.Exec(): %w", err)
	}
	log.Printf("pgxPool.Exec('INSERT INTO deviceid_nodeid(%x, %v)'; RowsAffected(): %v'", nodeID, deviceID, commandTag.RowsAffected())
	return nil
}

func getDeviceToken(nodeID []byte) (string, error) {
	var token string
	err := pgxPool.QueryRow(context.Background(),
		`SELECT deviceid
		  FROM deviceid_nodeid
		  WHERE nodeid=$1`, nodeID).Scan(&token)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func hasFilteredAddress(addrs []string) (bool, error) {
	var count int
	err := pgxPool.QueryRow(context.Background(),
		`SELECT count(*)
		  FROM filtered_addresses
		  WHERE address = ANY ($1)`, addrs).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func lspList(apiKeys []string) ([]string, error) {
	type void struct{}
	var member void

	rows, err := pgxPool.Query(context.Background(),
		`SELECT COALESCE(lsp_ids->'active', lsp_ids) as lsp_ids, api_user FROM api_keys
			WHERE api_key = ANY($1)`, apiKeys)
	if err != nil {
		log.Printf("error in pgxPool.Query: %v", err)
		return nil, fmt.Errorf("error in pgxPool.Query: %w", err)
	}
	defer rows.Close()
	lspSet := make(map[string]void)
	for rows.Next() {
		var lspIds []string
		var apiUser string
		err = rows.Scan(&lspIds, &apiUser)
		if err != nil {
			log.Printf("error in rows.Scan: %v", err)
			continue
		}
		log.Printf("ids: %#v, user: %#v", lspIds, apiUser)
		for _, l := range lspIds {
			lspSet[l] = member
		}
	}
	var lspList []string
	for l := range lspSet {
		lspList = append(lspList, l)
	}
	return lspList, nil
}

func lspFullList(apiKeys []string) ([]string, []string, error) {
	type void struct{}
	var member void

	rows, err := pgxPool.Query(context.Background(),
		`SELECT
			COALESCE(lsp_ids->'active', lsp_ids) as active_lsp_ids,
			COALESCE(lsp_ids->'inactive','[]') as inactive_lsp_ids,
			api_user
		FROM api_keys
        WHERE api_key = ANY($1)`, apiKeys)
	if err != nil {
		log.Printf("error in pgxPool.Query: %v", err)
		return nil, nil, fmt.Errorf("error in pgxPool.Query: %w", err)
	}
	defer rows.Close()
	var activeWithDups, inactiveWithDups []string
	for rows.Next() {
		var thisActive, thisInactive []string
		var apiUser string
		err = rows.Scan(&thisActive, &thisInactive, &apiUser)
		if err != nil {
			log.Printf("error in rows.Scan: %v", err)
			continue
		}
		log.Printf("active ids: %#v, inactive ids: %#v, user: %#v", thisActive, thisInactive, apiUser)
		activeWithDups = append(activeWithDups, thisActive...)
		inactiveWithDups = append(inactiveWithDups, thisInactive...)
	}
	lspSet := make(map[string]void)
	var active, inactive []string
	for _, l := range activeWithDups {
		if _, ok := lspSet[l]; !ok {
			active = append(active, l)
			lspSet[l] = member
		}
	}
	for _, l := range inactiveWithDups {
		if _, ok := lspSet[l]; !ok {
			inactive = append(inactive, l)
			lspSet[l] = member
		}
	}
	return active, inactive, nil
}
