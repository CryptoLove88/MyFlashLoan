package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Pool represents a Uniswap V3 pool
type Pool struct {
	ID              string `json:"id"`
	Token0          Token  `json:"token0"`
	Token1          Token  `json:"token1"`
	FeeTier         string `json:"feeTier"`
	VolumeUSD       string `json:"volumeUSD"`
	TxCount         string `json:"txCount"`
	TotalValueLockedUSD string `json:"totalValueLockedUSD"`
}

// Token represents a token in a pool
type Token struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Decimals string `json:"decimals"`
}

// GraphQLResponse represents the response from The Graph
type GraphQLResponse struct {
	Data struct {
		Pools []Pool `json:"pools"`
	} `json:"data"`
}

func main() {
	// Create a logger that writes to both console and file
	logFile, err := os.OpenFile("uniswap_pools.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}
	defer logFile.Close()

	logger := log.New(logFile, "", log.LstdFlags)

	// The Graph API endpoint for Uniswap V3
	url := "https://api.thegraph.com/subgraphs/name/ianlapham/uniswap-v3-mainnet"

	// GraphQL query to get the top 1000 pools by volume
	query := `
	{
		pools(first: 1000, orderBy: volumeUSD, orderDirection: desc) {
			id
			token0 {
				id
				symbol
				name
				decimals
			}
			token1 {
				id
				symbol
				name
				decimals
			}
			feeTier
			volumeUSD
			txCount
			totalValueLockedUSD
		}
	}
	`

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// Create request
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		logger.Fatalf("Failed to create request: %v", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")

	// Set request body
	reqBody := map[string]string{
		"query": query,
	}
	reqBodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		logger.Fatalf("Failed to marshal request body: %v", err)
	}
	req.Body = ioutil.NopCloser(strings.NewReader(string(reqBodyJSON)))

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		logger.Fatalf("Failed to send request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		logger.Fatalf("Failed to read response body: %v", err)
	}

	// Parse response
	var graphResp GraphQLResponse
	if err := json.Unmarshal(respBody, &graphResp); err != nil {
		logger.Fatalf("Failed to parse response: %v", err)
	}

	// Log the number of pools fetched
	logger.Printf("Fetched %d pools from Uniswap V3", len(graphResp.Data.Pools))

	// Save pools to a JSON file
	poolsJSON, err := json.MarshalIndent(graphResp.Data.Pools, "", "  ")
	if err != nil {
		logger.Fatalf("Failed to marshal pools: %v", err)
	}

	if err := ioutil.WriteFile("uniswap_pools.json", poolsJSON, 0644); err != nil {
		logger.Fatalf("Failed to write pools to file: %v", err)
	}

	// Print some statistics
	fmt.Printf("Top 10 Uniswap V3 Pools by Volume:\n")
	for i, pool := range graphResp.Data.Pools[:10] {
		fmt.Printf("%d. %s/%s (Fee: %s) - Volume: $%s, TVL: $%s\n",
			i+1,
			pool.Token0.Symbol,
			pool.Token1.Symbol,
			pool.FeeTier,
			pool.VolumeUSD,
			pool.TotalValueLockedUSD)
	}

	fmt.Printf("\nTotal pools fetched: %d\n", len(graphResp.Data.Pools))
	fmt.Printf("Data saved to uniswap_pools.json\n")
} 