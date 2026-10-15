// Package proxy implements the wire-protocol proxies that sit between an
// application and its database. Each engine gets its own subpackage
// (proxy/postgres, proxy/mysql, ...) that forwards traffic unchanged and
// emits core.Event values for every query it observes.
//
// The proxy is on the application's hot path: it must never block on
// analysis, storage or the studio. Events are handed off on a buffered
// channel and dropped (with a counter) if the consumer falls behind.
package proxy
