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

// toLevels: IOrderBookSide → [][]float64 через GetDataCopy() [][]any.
// Уровень = [price, amount]. Если тип поля в CCXT иной — CI-компилятор покажет, поправить здесь.
func toLevels(side interface{ GetDataCopy() [][]any }) [][]float64 {
	raw := side.GetDataCopy()
	out := make([][]float64, 0, len(raw))
	for _, lvl := range raw {
		if len(lvl) < 2 {
			continue
		}
		p, ok1 := asFloat(lvl[0])
		a, ok2 := asFloat(lvl[1])
		if !ok1 || !ok2 {
			continue
		}
		out = append(out, []float64{p, a})
	}
	return out
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
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

func watchOne(ctx context.Context, t target, out chan<- result, wg *sync.WaitGroup) {
	defer wg.Done()
	ex := ccxtpro.CreateExchange(t.exchange, nil)
	if ex == nil {
		out <- result{t: t, firstErr: "CreateExchange nil"}
		return
	}
	r := result{t: t, minSpread: 1e18}
	var prevMid float64
	for {
		select {
		case <-ctx.Done():
			out <- r
			return
		default:
		}
		ob, err := ex.WatchOrderBook(t.symbol)
		if err != nil {
			if r.firstErr == "" {
				r.firstErr = err.Error()
			}
			out <- r
			return
		}
		r.updates++
		bids := toLevels(ob.Bids)
		asks := toLevels(ob.Asks)
		r.depthBids = len(bids)
		r.depthAsks = len(asks)
		if len(bids) > 0 && len(asks) > 0 {
			bestBid, bestAsk := bids[0][0], asks[0][0]
			if bestAsk > 0 {
				spread := (bestAsk - bestBid) / bestAsk * 100
				if spread < r.minSpread {
					r.minSpread = spread
				}
				lastAsk := asks[len(asks)-1][0]
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

	fmt.Printf("%-14s %-5s %-16s %7s %6s %6s %6s %11s %s\n",
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
		fmt.Printf("%-14s %-5s %-16s %7d %6d %6d %6d %11.3f %s\n",
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
