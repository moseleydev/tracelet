// Command shop simulates the database traffic of a small online store, with
// the kinds of performance problems real apps have. Point it at Tracelet to
// see them in Tracelet Studio:
//
//	tracelet up --db postgres://localhost:5432/postgres
//	go run ./examples/shop --db postgres://localhost:6433/postgres
//
// It creates and seeds its own tables (prefixed shop_) on first run.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS shop_customers (
	id         int PRIMARY KEY,
	email      text NOT NULL,
	name       text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS shop_products (
	id       int PRIMARY KEY,
	name     text NOT NULL,
	category text NOT NULL,
	price    numeric(10, 2) NOT NULL,
	stock    int NOT NULL
);
-- Deliberately missing: an index on shop_orders(customer_id).
CREATE TABLE IF NOT EXISTS shop_orders (
	id          bigserial PRIMARY KEY,
	customer_id int NOT NULL,
	product_id  int NOT NULL,
	amount      numeric(10, 2) NOT NULL,
	status      text NOT NULL,
	created_at  timestamptz NOT NULL DEFAULT now()
);
-- Deliberately missing: an index on shop_reviews(product_id).
CREATE TABLE IF NOT EXISTS shop_reviews (
	id         bigserial PRIMARY KEY,
	product_id int NOT NULL,
	rating     int NOT NULL,
	body       text NOT NULL
);`

const seed = `
INSERT INTO shop_customers (id, email, name)
	SELECT g, 'customer' || g || '@example.com', 'Customer ' || g FROM generate_series(1, 20000) g;
INSERT INTO shop_products (id, name, category, price, stock)
	SELECT g, 'Product ' || g, (ARRAY['books','games','tools','garden','kitchen'])[1 + g % 5],
	       (random() * 200)::numeric(10, 2), 100 + (random() * 900)::int
	FROM generate_series(1, 2000) g;
INSERT INTO shop_orders (customer_id, product_id, amount, status, created_at)
	SELECT 1 + (random() * 19999)::int, 1 + (random() * 1999)::int, (random() * 300)::numeric(10, 2),
	       (ARRAY['paid','shipped','delivered','refunded'])[1 + (random() * 3)::int],
	       now() - random() * interval '365 days'
	FROM generate_series(1, 300000);
INSERT INTO shop_reviews (product_id, rating, body)
	SELECT 1 + (random() * 1999)::int, 1 + (random() * 4)::int, md5(random()::text)
	FROM generate_series(1, 150000);
ANALYZE;`

type endpoint struct {
	name   string
	weight int
	run    func(ctx context.Context, db *pgxpool.Pool) error
}

var endpoints = []endpoint{
	{"GET /products/:id", 30, func(ctx context.Context, db *pgxpool.Pool) error {
		// Fast: primary-key lookup.
		var name string
		return db.QueryRow(ctx, tag(ctx, `SELECT name FROM shop_products WHERE id = $1`), 1+mrand.IntN(2000)).Scan(&name)
	}},
	{"GET /customers/:id/orders", 15, func(ctx context.Context, db *pgxpool.Pool) error {
		// Slow: no index on customer_id, so every call scans 300k orders.
		rows, err := db.Query(ctx, tag(ctx, `SELECT id, amount, status FROM shop_orders WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 20`),
			1+mrand.IntN(20000))
		if err != nil {
			return err
		}
		rows.Close()
		return rows.Err()
	}},
	{"GET /orders/recent", 8, func(ctx context.Context, db *pgxpool.Pool) error {
		// N+1: one query for the page, then one per order for its customer.
		rows, err := db.Query(ctx, tag(ctx, `SELECT customer_id FROM shop_orders ORDER BY id DESC LIMIT 20`))
		if err != nil {
			return err
		}
		var ids []int
		for rows.Next() {
			var id int
			rows.Scan(&id)
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			var email string
			if err := db.QueryRow(ctx, tag(ctx, `SELECT email FROM shop_customers WHERE id = $1`), id).Scan(&email); err != nil {
				return err
			}
		}
		return nil
	}},
	{"GET /products/:id/reviews", 10, func(ctx context.Context, db *pgxpool.Pool) error {
		// Slow: aggregate over an unindexed column.
		var avg *float64
		var n int
		return db.QueryRow(ctx, tag(ctx, `SELECT avg(rating), count(*) FROM shop_reviews WHERE product_id = $1`),
			1+mrand.IntN(2000)).Scan(&avg, &n)
	}},
	{"GET /search", 5, func(ctx context.Context, db *pgxpool.Pool) error {
		// Slow: leading wildcard defeats any index.
		rows, err := db.Query(ctx, tag(ctx, `SELECT id, email FROM shop_customers WHERE email LIKE '%' || $1 || '%' LIMIT 10`),
			fmt.Sprint(mrand.IntN(999)))
		if err != nil {
			return err
		}
		rows.Close()
		return rows.Err()
	}},
	{"POST /checkout", 6, func(ctx context.Context, db *pgxpool.Pool) error {
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		product := 1 + mrand.IntN(2000)
		if _, err := tx.Exec(ctx, tag(ctx, `UPDATE shop_products SET stock = stock - 1 WHERE id = $1`), product); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, tag(ctx, `INSERT INTO shop_orders (customer_id, product_id, amount, status) VALUES ($1, $2, $3, 'paid')`),
			1+mrand.IntN(20000), product, mrand.Float64()*300); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}},
	{"GET /categories", 12, func(ctx context.Context, db *pgxpool.Pool) error {
		rows, err := db.Query(ctx, tag(ctx, `SELECT category, count(*) FROM shop_products GROUP BY category`))
		if err != nil {
			return err
		}
		rows.Close()
		return rows.Err()
	}},
	{"POST /coupons/apply", 1, func(ctx context.Context, db *pgxpool.Pool) error {
		// Broken: the coupons table was never created.
		var pct int
		db.QueryRow(ctx, tag(ctx, `SELECT percent FROM shop_coupons WHERE code = $1`), "SAVE10").Scan(&pct)
		return nil // the app swallows this error; Tracelet still sees it
	}},
}

func main() {
	dbURL := flag.String("db", "postgres://localhost:6433/postgres", "database URL; point it at the Tracelet proxy")
	rate := flag.Float64("rate", 20, "requests per second")
	workers := flag.Int("workers", 4, "concurrent connections")
	duration := flag.Duration("duration", 0, "how long to run; 0 runs until Ctrl+C")
	reset := flag.Bool("reset", false, "drop and re-seed the shop_ tables first")
	trace := flag.Bool("trace", true, "tag queries with their request route and trace id (sqlcommenter), like the Tracelet SDKs do")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	cfg, err := pgxpool.ParseConfig(*dbURL)
	if err != nil {
		log.Fatal(err)
	}
	cfg.MaxConns = int32(*workers)
	if *trace {
		// Every tagged query text is unique (it carries a trace id), so skip
		// pgx's per-text prepared statement cache.
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := prepare(context.Background(), db, *reset); err != nil {
		log.Fatal(err)
	}

	total := 0
	for _, e := range endpoints {
		total += e.weight
	}
	pick := func() endpoint {
		n := mrand.IntN(total)
		for _, e := range endpoints {
			if n < e.weight {
				return e
			}
			n -= e.weight
		}
		return endpoints[0]
	}

	var served, failed atomic.Int64
	jobs := make(chan endpoint)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				reqCtx := ctx
				if *trace {
					reqCtx = withRequest(ctx, e.name)
				}
				if err := e.run(reqCtx, db); err != nil && ctx.Err() == nil {
					failed.Add(1)
					log.Printf("%s: %v", e.name, err)
				}
				served.Add(1)
			}
		}()
	}

	fmt.Printf("shop: sending ~%.0f requests/s; press Ctrl+C to stop\n", *rate)
	tick := time.NewTicker(time.Duration(float64(time.Second) / *rate))
	report := time.NewTicker(5 * time.Second)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-report.C:
			fmt.Printf("shop: %d requests served, %d failed\n", served.Load(), failed.Load())
		case <-tick.C:
			select {
			case jobs <- pick():
			default: // all workers busy: the database is the bottleneck
			}
		}
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("shop: done, %d requests served\n", served.Load())
}

func prepare(ctx context.Context, db *pgxpool.Pool, reset bool) error {
	if reset {
		if _, err := db.Exec(ctx, tag(ctx, `DROP TABLE IF EXISTS shop_customers, shop_products, shop_orders, shop_reviews`)); err != nil {
			return err
		}
	}
	if _, err := db.Exec(ctx, schema); err != nil {
		return fmt.Errorf("create tables: %w", err)
	}
	var n int
	if err := db.QueryRow(ctx, tag(ctx, `SELECT count(*) FROM shop_customers`)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	fmt.Println("shop: seeding tables (about 10 seconds)...")
	if _, err := db.Exec(ctx, seed); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}

// Request context, tagged onto queries the way the Tracelet SDKs do it:
// a sqlcommenter comment with the route and a W3C traceparent.

type requestKey struct{}

func withRequest(ctx context.Context, route string) context.Context {
	trace, span := make([]byte, 16), make([]byte, 8)
	rand.Read(trace)
	rand.Read(span)
	tp := "00-" + hex.EncodeToString(trace) + "-" + hex.EncodeToString(span) + "-01"
	return context.WithValue(ctx, requestKey{}, fmt.Sprintf("/*route='%s',traceparent='%s'*/", url.PathEscape(route), tp))
}

func tag(ctx context.Context, sql string) string {
	if c, ok := ctx.Value(requestKey{}).(string); ok {
		return sql + " " + c
	}
	return sql
}
