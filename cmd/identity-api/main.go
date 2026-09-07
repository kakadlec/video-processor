package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// signal arrives. This service's two routes are a bcrypt hash and a database
// round trip, so it is generous rather than tight for the same reason a
// tighter one would buy nothing.
const shutdownTimeout = 30 * time.Second

// readHeaderTimeout bounds how long a client may take to send its request
// headers.
const readHeaderTimeout = 10 * time.Second

func main() {
	ctx := context.Background()

	identity, identityDB, err := setupIdentity(ctx)
	if err != nil {
		log.Fatal(err)
	}

	r := setupRouter(identity)

	// Signal-aware rather than log.Fatal(r.Run(...)): that exits through
	// os.Exit, which runs no deferred call, so an in-flight registration
	// would be cut off mid-transaction rather than allowed to finish.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	server := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	fmt.Println("🔐 Identity API listening on port 8080")

	serverFailed := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverFailed <- err
		}
		close(serverFailed)
	}()

	select {
	case err := <-serverFailed:
		if err != nil {
			log.Printf("http server: %v", err)
		}
	case <-signalCtx.Done():
		log.Print("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server shutdown: %v", err)
	}

	// Shutdown then close, and there is no ordering constraint hiding in
	// that: unlike cmd/api's sequence, nothing here borrows the pool for a
	// whole operation. There is no relay and no background goroutine holding
	// a transaction, so once Shutdown has returned, every statement this
	// process will ever run has finished.
	closeDB(identityDB)
}

func setupRouter(identity *identityModule) *gin.Engine {
	r := gin.Default()

	// Byte-identical to the other services' CORS middleware, deliberately.
	// A browser reaches all of them through one origin, so advertising a
	// different policy per path would make the split observable to a client
	// for the first time. PUT is advertised here for a route this service
	// does not serve, and that is the point.
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// No bearer-auth group and no rate limiter, matching what cmd/api does
	// for these two routes today: they are how a caller obtains a token, so
	// requiring one would be circular.
	identity.registerRoutes(r)

	return r
}
