package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mixigroup/mixi2-application-sample-go/config"
	"github.com/mixigroup/mixi2-application-sample-go/handler"
	"github.com/mixigroup/mixi2-application-sdk-go/auth"
	"github.com/mixigroup/mixi2-application-sdk-go/event/webhook"
	application_apiv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	cfg := config.GetConfig()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Decode public key
	if cfg.SignaturePublicKey == "" {
		log.Fatal("SIGNATURE_PUBLIC_KEY is required")
	}
	publicKeyBytes, err := base64.StdEncoding.DecodeString(cfg.SignaturePublicKey)
	if err != nil {
		log.Fatalf("failed to decode public key: %v", err)
	}
	publicKey := ed25519.PublicKey(publicKeyBytes)

	// Create authenticator
	authenticator, err := auth.NewAuthenticator(cfg.ClientID, cfg.ClientSecret, cfg.TokenURL)
	if err != nil {
		log.Fatalf("failed to create authenticator: %v", err)
	}

	// Create gRPC connection for API
	apiConn, err := grpc.NewClient(
		cfg.APIAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
	)
	if err != nil {
		log.Fatalf("failed to connect to api: %v", err)
	}
	defer apiConn.Close()

	// Create API client
	apiClient := application_apiv1.NewApplicationServiceClient(apiConn)

	// Create event handler
	eventHandler := handler.NewHandler(apiClient, authenticator)

	// Create server
	addr := ":" + cfg.Port
	server := webhook.NewServer(addr, publicKey, eventHandler, webhook.WithLogger(logger))

	// Setup graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shutdown error", slog.Any("error", err))
		}
	}()

	// Start server
	logger.Info("starting webhook server", slog.String("port", cfg.Port))
	if err := server.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
	logger.Info("stopped")
}
