package main

import (
	"context"
	"crypto/tls"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mixigroup/mixi2-application-sample-go/config"
	"github.com/mixigroup/mixi2-application-sample-go/handler"
	"github.com/mixigroup/mixi2-application-sdk-go/auth"
	"github.com/mixigroup/mixi2-application-sdk-go/event/stream"
	application_apiv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_api/v1"
	application_streamv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_stream/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	cfg := config.GetConfig()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	// Create authenticator
	authenticator, err := auth.NewAuthenticator(cfg.ClientID, cfg.ClientSecret, cfg.TokenURL)
	if err != nil {
		log.Fatalf("failed to create authenticator: %v", err)
	}

	// Create gRPC connection for stream
	streamConn, err := grpc.NewClient(
		cfg.StreamAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
	)
	if err != nil {
		log.Fatalf("failed to connect to stream: %v", err)
	}
	defer streamConn.Close()

	// Create gRPC connection for API
	apiConn, err := grpc.NewClient(
		cfg.APIAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
	)
	if err != nil {
		log.Fatalf("failed to connect to api: %v", err)
	}
	defer apiConn.Close()

	// Create stream client and watcher
	streamClient := application_streamv1.NewApplicationServiceClient(streamConn)
	watcher := stream.NewStreamWatcher(streamClient, authenticator, stream.WithLogger(logger))

	// Create API client
	apiClient := application_apiv1.NewApplicationServiceClient(apiConn)

	// Create event handler
	eventHandler := handler.NewHandler(apiClient, authenticator)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down...")
		cancel()
	}()

	// Start watching
	logger.Info("starting stream watcher", slog.String("address", cfg.StreamAddress))
	if err := watcher.Watch(ctx, eventHandler); err != nil {
		if err != context.Canceled {
			log.Fatalf("watcher error: %v", err)
		}
	}
	logger.Info("stopped")
}
