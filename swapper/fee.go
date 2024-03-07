package swapper

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type FeeService struct {
	feesLastUpdated time.Time
	currentFees     *whatthefeeBody
	mtx             sync.RWMutex
}

type whatthefeeBody struct {
	Index   []int32   `json:"index"`
	Columns []string  `json:"columns"`
	Data    [][]int32 `json:"data"`
}

func NewFeeService() *FeeService {
	return &FeeService{}
}

func (f *FeeService) Start(ctx context.Context) {
	go f.watchFeeRate(ctx)
}

func (f *FeeService) GetFeeRate(blocks int32) (float64, error) {
	f.mtx.RLock()
	defer f.mtx.RUnlock()

	if f.currentFees == nil {
		return 0, fmt.Errorf("still no fees")
	}

	if len(f.currentFees.Index) < 1 {
		return 0, fmt.Errorf("empty row index")
	}

	// get the block between 0 and SwapLockTime
	b := math.Min(math.Max(0, float64(blocks)), SwapLockTime)

	// certainty is linear between 0.5 and 1 based on the amount of blocks left
	certainty := 0.5 + (((SwapLockTime - b) / SwapLockTime) / 2)

	// Get the row closest to the amount of blocks left
	rowIndex := 0
	prevRow := f.currentFees.Index[rowIndex]
	for i := 1; i < len(f.currentFees.Index); i++ {
		current := f.currentFees.Index[i]
		if math.Abs(float64(current)-b) < math.Abs(float64(prevRow)-b) {
			rowIndex = i
			prevRow = current
		}
	}

	if len(f.currentFees.Columns) < 1 {
		return 0, fmt.Errorf("empty column index")
	}

	// Get the column closest to the certainty
	columnIndex := 0
	prevColumn, err := strconv.ParseFloat(f.currentFees.Columns[columnIndex], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid column content '%s'", f.currentFees.Columns[columnIndex])
	}
	for i := 1; i < len(f.currentFees.Columns); i++ {
		current, err := strconv.ParseFloat(f.currentFees.Columns[i], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid column content '%s'", f.currentFees.Columns[i])
		}
		if math.Abs(current-certainty) < math.Abs(prevColumn-certainty) {
			columnIndex = i
			prevColumn = current
		}
	}

	if rowIndex >= len(f.currentFees.Data) {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}
	row := f.currentFees.Data[rowIndex]
	if columnIndex >= len(row) {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}

	rate := row[columnIndex]
	satPerVByte := math.Exp(float64(rate) / 100)
	return satPerVByte, nil
}

func (f *FeeService) watchFeeRate(ctx context.Context) {
	for {
		now := time.Now()
		fees, err := f.getFees()
		if err != nil {
			log.Printf("failed to get current chain fee rates: %v", err)
		} else {
			f.mtx.Lock()
			f.currentFees = fees
			f.feesLastUpdated = now
			f.mtx.Unlock()
		}

		select {
		case <-time.After(time.Minute * 5):
		case <-ctx.Done():
			return
		}
	}
}

func (r *FeeService) getFees() (*whatthefeeBody, error) {
	now := time.Now().Unix()
	cacheBust := (now / 300) * 300
	resp, err := http.Get(
		fmt.Sprintf("https://whatthefee.io/data.json?c=%d", cacheBust),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to call whatthefee.io: %v", err)
	}
	defer resp.Body.Close()

	var body whatthefeeBody
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode whatthefee.io response: %w", err)
	}

	return &body, nil
}
