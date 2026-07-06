package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	ccxtpro "github.com/ccxt/ccxt/go/v4/pro"
)

// 12 монет: 10 неликвидов (rank 301-998) + BTC/ETH контроль. Из builder/coinmap.json.
var coins = []string{
	"BTC", "ETH", // ликвидный контроль
	"EGLD", "ZAMA", "MEGA", "DEEP", "JCT", "ROBO", "TRUTH", "BREV", "HMSTR", "ORDER",
}

// биржи билдера. KuCoin perp = отдельный id kucoinfutures.
var exchanges = []string{"bybit", "kucoin", "mexc", "bingx"}

type target struct {
	exchange string
	market   string // "spot" | "perp"
	symbol   string // формат CCXT
}

type result struct {
	t           target
	updates     int
	depthBids   int
	depthAsks   int
	jumpBad     int     // число >5% скачков мида (H1)
	maxDepthPct float64 // докуда дотянулась ask-книга: (last_ask-best_ask)/best_ask*100 (H2)
	minSpread   float64
	firstErr    string
}

func buildTargets(exchange string) []target {
	perpEx := exchange
	if exchange == "kucoin" {
		perpEx = "kucoinfutures"
	}
	var ts []target
	for _, c := range coins {
		ts = append(ts,
			target{exchange, "spot", c + "/USDT"},
			target{perpEx, "perp", c + "/USDT:USDT"},
		)
	}
	return ts
}

// limitFor возвращает max допустимую глубину книги для биржи.
// Bybit тянет 1000; KuCoin/MEXC/BingX — потолок 100 (проверено зондом).
func limitFor(exchange string) int64 {
	if exchange == "bybit" {
		return 1000
	}
	return 100
}

func watchOne(ctx context.Context, t target, out chan<- result, wg *sync.WaitGroup) {
	defer wg.Done()
	r := result{t: t, minSpread: 1e18}
	defer func() {
		if rec := recover(); rec != nil {
			if r.firstErr == "" {
				r.firstErr = fmt.Sprintf("panic: %v", rec)
			}
			out <- r
		}
	}()
	ex := ccxtpro.CreateExchange(t.exchange, nil)
	if ex == nil {
		r.firstErr = "CreateExchange nil"
		out <- r
		return
	}
	var prevMid float64
	for {
		select {
		case <-ctx.Done():
			out <- r
			return
		default:
		}
		ob, err := ex.WatchOrderBook(t.symbol, ccxtpro.WithWatchOrderBookLimit(limitFor(t.exchange)))
		if err != nil {
			if r.firstErr == "" {
				r.firstErr = err.Error()
			}
			out <- r
			return
		}
		r.updates++
		r.depthBids = len(ob.Bids)
		r.depthAsks = len(ob.Asks)
		if len(ob.Bids) > 0 && len(ob.Asks) > 0 {
			bestBid, bestAsk := ob.Bids[0][0], ob.Asks[0][0]
			if bestAsk > 0 {
				spread := (bestAsk - bestBid) / bestAsk * 100
				if spread < r.minSpread {
					r.minSpread = spread
				}
				lastAsk := ob.Asks[len(ob.Asks)-1][0]
				r.maxDepthPct = (lastAsk - bestAsk) / bestAsk * 100
			}
			mid := (bestBid + bestAsk) / 2
			if prevMid > 0 {
				j := (mid - prevMid) / prevMid
				if j < 0 {
					j = -j
				}
				if j > 0.05 {
					r.jumpBad++
				}
			}
			prevMid = mid
		}
	}
}

func runExchange(exchange string, window time.Duration) {
	ts := buildTargets(exchange)
	log.Printf("=== %s: %d целей, окно %s ===", exchange, len(ts), window)
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()
	out := make(chan result, len(ts))
	var wg sync.WaitGroup
	for _, t := range ts {
		wg.Add(1)
		go watchOne(ctx, t, out, &wg)
	}
	go func() { wg.Wait(); close(out) }()

	results := map[string]result{}
	for r := range out {
		results[r.t.exchange+"|"+r.t.market+"|"+r.t.symbol] = r
	}

	fmt.Printf("@@ROW@@ %-14s %-5s %-16s %7s %6s %6s %6s %11s %s\n",
		"EXCHANGE", "MKT", "SYMBOL", "UPD", "DBID", "DASK", "JUMPS", "MAXDEPTH%", "STATUS")
	for _, t := range ts {
		r, ok := results[t.exchange+"|"+t.market+"|"+t.symbol]
		if !ok {
			continue
		}
		status := "OK"
		if r.firstErr != "" {
			status = "ERR:" + r.firstErr
		} else if r.updates == 0 {
			status = "МОЛЧИТ"
		} else if r.jumpBad > 0 {
			status = "ПРЫГАЕТ"
		}
		fmt.Printf("@@ROW@@ %-14s %-5s %-16s %7d %6d %6d %6d %11.3f %s\n",
			r.t.exchange, r.t.market, r.t.symbol, r.updates,
			r.depthBids, r.depthAsks, r.jumpBad, r.maxDepthPct, status)
	}
	fmt.Println()
}

func main() {
	window := 25 * time.Second
	only := ""
	if len(os.Args) > 1 {
		only = os.Args[1]
	}
	for _, ex := range exchanges {
		if only != "" && ex != only {
			continue
		}
		runExchange(ex, window)
	}
}
