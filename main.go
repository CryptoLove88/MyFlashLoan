package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

// PoolConfig represents a pool configuration
type PoolConfig struct {
	Address    string         `json:"address"`
	Token0     string         `json:"token0"`
	Token1     string         `json:"token1"`
	Token0Addr common.Address `json:"token0_address"`
	Token1Addr common.Address `json:"token1_address"`
	Decimals0  int            `json:"decimals0"`
	Decimals1  int            `json:"decimals1"`
	Type       string         `json:"type"` // "uniswap" or "curve"
}

// AppConfig represents the application configuration
type AppConfig struct {
	Infura struct {
		WSURL string `json:"ws_url"`
	} `json:"infura"`
	Pools map[string]PoolConfig `json:"pools"`
}

// PriceData represents the latest price data for a pool
type PriceData struct {
	Price     *big.Float
	Direction string
	UpdatedAt int64
}

// TokenInfo represents token information
type TokenInfo struct {
	Address  common.Address
	Decimals int
	Symbol   string
}

// PriceInfo represents price information for a specific pool
type PriceInfo struct {
	DexType   string     // "uniswap" or "curve"
	PoolAddr  string     // pool address
	Price     *big.Float // price value
	LogPrice  *big.Float // natural logarithm of price
	UpdatedAt int64      // timestamp of last update
}

// BestPriceInfo represents the best price information for a token pair
type BestPriceInfo struct {
	Forward   *PriceInfo // best price for token0/token1
	Reverse   *PriceInfo // best price for token1/token0
	UpdatedAt int64      // timestamp of last update
}

var (
	config     AppConfig
	uniswapABI abi.ABI
	curveABI   abi.ABI
	logger     *log.Logger
	priceData  = make(map[string]*PriceData)
	priceMutex sync.RWMutex

	// Price caches
	priceCache     = make(map[string][]*PriceInfo)   // key: "TOKEN0_TOKEN1"
	bestPriceCache = make(map[string]*BestPriceInfo) // key: "TOKEN0_TOKEN1"
	cacheMutex     sync.RWMutex
)

func init() {
	// Load ABIs
	loadABIs()

	// Setup logger
	setupLogger()

	// Load configuration
	loadConfig()
}

func loadABIs() {
	// Load Uniswap ABI
	abiFile, err := os.Open("uniswap_pair_abi.json")
	if err != nil {
		log.Fatalf("Failed to open Uniswap ABI file: %v", err)
	}
	defer abiFile.Close()

	abiBytes, err := io.ReadAll(abiFile)
	if err != nil {
		log.Fatalf("Failed to read Uniswap ABI file: %v", err)
	}

	uniswapABI, err = abi.JSON(strings.NewReader(string(abiBytes)))
	if err != nil {
		log.Fatalf("Failed to parse Uniswap ABI: %v", err)
	}

	// Load Curve ABI
	abiFile, err = os.Open("curve_pair_abi.json")
	if err != nil {
		log.Fatalf("Failed to open Curve ABI file: %v", err)
	}
	defer abiFile.Close()

	abiBytes, err = io.ReadAll(abiFile)
	if err != nil {
		log.Fatalf("Failed to read Curve ABI file: %v", err)
	}

	curveABI, err = abi.JSON(strings.NewReader(string(abiBytes)))
	if err != nil {
		log.Fatalf("Failed to parse Curve ABI: %v", err)
	}
}

func setupLogger() {
	logFile, err := os.OpenFile("arbitrage.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	logger = log.New(io.MultiWriter(os.Stdout, logFile), "", log.LstdFlags)
}

func loadConfig() {
	configFile, err := os.Open("config.json")
	if err != nil {
		logger.Fatalf("Failed to open config file: %v", err)
	}
	defer configFile.Close()

	configBytes, err := io.ReadAll(configFile)
	if err != nil {
		logger.Fatalf("Failed to read config file: %v", err)
	}

	if err := json.Unmarshal(configBytes, &config); err != nil {
		logger.Fatalf("Failed to parse config file: %v", err)
	}
}

func main() {
	// Connect to Infura WebSocket
	client, err := ethclient.Dial(config.Infura.WSURL)
	if err != nil {
		logger.Fatalf("Failed to connect to Infura: %v", err)
	}
	defer client.Close()

	// Get initial prices for all pools
	getInitialPrices(client)

	// Subscribe to all pools
	subscribeToPools(client)

	// Keep the program running
	select {}
}

func getInitialPrices(client *ethclient.Client) {
	for poolAddr, pool := range config.Pools {
		if pool.Type == "uniswap" {
			getUniswapPrice(client, poolAddr, pool)
		} else if pool.Type == "curve" {
			getCurvePrice(client, poolAddr, pool)
		}
	}
}

func getUniswapPrice(client *ethclient.Client, poolAddr string, pool PoolConfig) {
	// Get the latest block
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		logger.Printf("Failed to get latest block for %s: %v", poolAddr, err)
		return
	}

	// Get the latest event
	query := ethereum.FilterQuery{
		Addresses: []common.Address{common.HexToAddress(poolAddr)},
		Topics:    [][]common.Hash{{uniswapABI.Events["Swap"].ID}},
		FromBlock: header.Number,
		ToBlock:   header.Number,
	}

	logs, err := client.FilterLogs(context.Background(), query)
	if err != nil {
		logger.Printf("Failed to get logs for %s: %v", poolAddr, err)
		return
	}

	if len(logs) > 0 {
		handleUniswapEvent(logs[len(logs)-1], pool)
	}
}

func getCurvePrice(client *ethclient.Client, poolAddr string, pool PoolConfig) {
	// Get the latest block
	header, err := client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		logger.Printf("Failed to get latest block for %s: %v", poolAddr, err)
		return
	}

	// Get the latest event
	query := ethereum.FilterQuery{
		Addresses: []common.Address{common.HexToAddress(poolAddr)},
		Topics:    [][]common.Hash{{curveABI.Events["TokenExchange"].ID}},
		FromBlock: header.Number,
		ToBlock:   header.Number,
	}

	logs, err := client.FilterLogs(context.Background(), query)
	if err != nil {
		logger.Printf("Failed to get logs for %s: %v", poolAddr, err)
		return
	}

	if len(logs) > 0 {
		handleCurveEvent(logs[len(logs)-1], pool)
	}
}

func subscribeToPools(client *ethclient.Client) {
	for poolAddr, pool := range config.Pools {
		if pool.Type == "uniswap" {
			subscribeToUniswapPool(client, poolAddr, pool)
		} else if pool.Type == "curve" {
			subscribeToCurvePool(client, poolAddr, pool)
		}
	}
}

func subscribeToUniswapPool(client *ethclient.Client, poolAddr string, pool PoolConfig) {
	logs := make(chan types.Log)
	sub, err := client.SubscribeFilterLogs(context.Background(), ethereum.FilterQuery{
		Addresses: []common.Address{common.HexToAddress(poolAddr)},
		Topics:    [][]common.Hash{{uniswapABI.Events["Swap"].ID}},
	}, logs)
	if err != nil {
		logger.Printf("Failed to subscribe to Uniswap pool %s: %v", poolAddr, err)
		return
	}

	go func() {
		for {
			select {
			case err := <-sub.Err():
				logger.Printf("Subscription error for %s: %v", poolAddr, err)
			case vLog := <-logs:
				handleUniswapEvent(vLog, pool)
			}
		}
	}()
}

func subscribeToCurvePool(client *ethclient.Client, poolAddr string, pool PoolConfig) {
	logs := make(chan types.Log)
	sub, err := client.SubscribeFilterLogs(context.Background(), ethereum.FilterQuery{
		Addresses: []common.Address{common.HexToAddress(poolAddr)},
		Topics:    [][]common.Hash{{curveABI.Events["TokenExchange"].ID}},
	}, logs)
	if err != nil {
		logger.Printf("Failed to subscribe to Curve pool %s: %v", poolAddr, err)
		return
	}

	go func() {
		for {
			select {
			case err := <-sub.Err():
				logger.Printf("Subscription error for %s: %v", poolAddr, err)
			case vLog := <-logs:
				handleCurveEvent(vLog, pool)
			}
		}
	}()
}

func updatePriceCache(pool PoolConfig, price *big.Float, dexType string) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()

	// Create forward and reverse pair names
	forwardPair := fmt.Sprintf("%s_%s", pool.Token0, pool.Token1)
	reversePair := fmt.Sprintf("%s_%s", pool.Token1, pool.Token0)

	// Calculate natural logarithm of price
	price64, _ := price.Float64()
	logPrice := new(big.Float).SetFloat64(math.Log(price64))

	// Create price info
	priceInfo := &PriceInfo{
		DexType:   dexType,
		PoolAddr:  pool.Address,
		Price:     price,
		LogPrice:  logPrice,
		UpdatedAt: time.Now().Unix(),
	}

	// Update price cache
	// Forward direction
	if _, exists := priceCache[forwardPair]; !exists {
		priceCache[forwardPair] = make([]*PriceInfo, 0)
	}
	// Remove old price info for this pool if exists
	for i, info := range priceCache[forwardPair] {
		if info.PoolAddr == pool.Address {
			priceCache[forwardPair] = append(priceCache[forwardPair][:i], priceCache[forwardPair][i+1:]...)
			break
		}
	}
	priceCache[forwardPair] = append(priceCache[forwardPair], priceInfo)

	// Reverse direction
	reversePrice := new(big.Float).Quo(big.NewFloat(1), price)
	reverseLogPrice := new(big.Float).Neg(logPrice)
	reversePriceInfo := &PriceInfo{
		DexType:   dexType,
		PoolAddr:  pool.Address,
		Price:     reversePrice,
		LogPrice:  reverseLogPrice,
		UpdatedAt: time.Now().Unix(),
	}

	if _, exists := priceCache[reversePair]; !exists {
		priceCache[reversePair] = make([]*PriceInfo, 0)
	}
	// Remove old price info for this pool if exists
	for i, info := range priceCache[reversePair] {
		if info.PoolAddr == pool.Address {
			priceCache[reversePair] = append(priceCache[reversePair][:i], priceCache[reversePair][i+1:]...)
			break
		}
	}
	priceCache[reversePair] = append(priceCache[reversePair], reversePriceInfo)

	// Update best price cache
	// Forward direction
	if _, exists := bestPriceCache[forwardPair]; !exists {
		bestPriceCache[forwardPair] = &BestPriceInfo{}
	}
	bestForward := bestPriceCache[forwardPair]
	if bestForward.Forward == nil || logPrice.Cmp(bestForward.Forward.LogPrice) > 0 {
		bestForward.Forward = priceInfo
		bestForward.UpdatedAt = time.Now().Unix()
	}

	// Reverse direction
	if _, exists := bestPriceCache[reversePair]; !exists {
		bestPriceCache[reversePair] = &BestPriceInfo{}
	}
	bestReverse := bestPriceCache[reversePair]
	if bestReverse.Reverse == nil || reverseLogPrice.Cmp(bestReverse.Reverse.LogPrice) > 0 {
		bestReverse.Reverse = reversePriceInfo
		bestReverse.UpdatedAt = time.Now().Unix()
	}
}

func handleUniswapEvent(vLog types.Log, pool PoolConfig) {
	// Unpack the event
	var event struct {
		Amount0      *big.Int
		Amount1      *big.Int
		SqrtPriceX96 *big.Int
		Liquidity    *big.Int
		Tick         *big.Int
	}

	err := uniswapABI.UnpackIntoInterface(&event, "Swap", vLog.Data)
	if err != nil {
		logger.Printf("Failed to unpack Uniswap event: %v", err)
		return
	}

	// Calculate price using SqrtPriceX96
	// price = (SqrtPriceX96 / 2^96)^2
	sqrtPrice := new(big.Float).SetInt(event.SqrtPriceX96)
	divisor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(2), big.NewInt(96), nil))
	sqrtPriceFloat := new(big.Float).Quo(sqrtPrice, divisor)
	price := new(big.Float).Mul(sqrtPriceFloat, sqrtPriceFloat)

	// Adjust for token decimals
	decimalsDiff := pool.Decimals0 - pool.Decimals1
	if decimalsDiff > 0 {
		adjustment := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimalsDiff)), nil))
		price = new(big.Float).Mul(price, adjustment)
	} else if decimalsDiff < 0 {
		adjustment := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-decimalsDiff)), nil))
		price = new(big.Float).Quo(price, adjustment)
	}

	// Update price cache
	updatePriceCache(pool, price, "uniswap")

	// Log the price update
	logger.Printf("Uniswap %s_%s Price Update - %s/%s: %f (Tick: %d, Direction: %s -> %s)",
		pool.Token0, pool.Token1,
		pool.Token0, pool.Token1,
		price,
		event.Tick,
		pool.Token0, pool.Token1)
}

func handleCurveEvent(vLog types.Log, pool PoolConfig) {
	// Unpack the event
	var event struct {
		Buyer        common.Address
		SoldID        *big.Int `abi:"sold_id"`
		TokensSold    *big.Int `abi:"tokens_sold"`
		BoughtID      *big.Int `abi:"bought_id"`
		TokensBought  *big.Int `abi:"tokens_bought"`
	}

	err := curveABI.UnpackIntoInterface(&event, "TokenExchange", vLog.Data)
	if err != nil {
		logger.Printf("Failed to unpack Curve event: %v", err)
		return
	}

	// Calculate price
	price := new(big.Float).SetInt(event.TokensSold)
	price = new(big.Float).Quo(price, new(big.Float).SetInt(event.TokensBought))

	// Adjust for token decimals
	decimalsDiff := pool.Decimals1 - pool.Decimals0
	if decimalsDiff != 0 {
		adjustment := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimalsDiff)), nil))
		price = new(big.Float).Mul(price, adjustment)
	}

	// Update price cache
	updatePriceCache(pool, price, "curve")

	// Log the price update
	logger.Printf("Curve %s_%s Price Update - %s/%s: %f (Sold ID: %d, Bought ID: %d, Direction: %s -> %s)",
		pool.Token0, pool.Token1,
		pool.Token0, pool.Token1,
		price,
		event.SoldID,
		event.BoughtID,
		pool.Token0, pool.Token1)
}
