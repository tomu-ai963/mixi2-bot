package handler

import (
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	apphandler "github.com/mixigroup/mixi2-application-sample-go/handler"
	"github.com/mixigroup/mixi2-application-sdk-go/auth"
	"github.com/mixigroup/mixi2-application-sdk-go/event/webhook"
	application_apiv1 "github.com/mixigroup/mixi2-application-sdk-go/gen/go/social/mixi/application/service/application_api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	once           sync.Once
	eventHandlerFn http.HandlerFunc
	initErr        error
	apiConn        *grpc.ClientConn
)

func setup() {
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")
	tokenURL := os.Getenv("TOKEN_URL")
	apiAddress := os.Getenv("API_ADDRESS")
	signaturePubKey := os.Getenv("SIGNATURE_PUBLIC_KEY")

	log.Printf("setup start: CLIENT_ID=%t CLIENT_SECRET=%t TOKEN_URL=%t API_ADDRESS=%t SIGNATURE_PUBLIC_KEY=%t",
		clientID != "", clientSecret != "", tokenURL != "", apiAddress != "", signaturePubKey != "")

	if clientID == "" || clientSecret == "" || tokenURL == "" || apiAddress == "" || signaturePubKey == "" {
		initErr = fmt.Errorf("missing required environment variables")
		return
	}

	publicKeyBytes, err := base64.StdEncoding.DecodeString(signaturePubKey)
	if err != nil {
		initErr = fmt.Errorf("failed to decode SIGNATURE_PUBLIC_KEY: %w", err)
		return
	}
	publicKey := ed25519.PublicKey(publicKeyBytes)

	authenticator, err := auth.NewAuthenticator(clientID, clientSecret, tokenURL)
	if err != nil {
		initErr = fmt.Errorf("failed to create authenticator: %w", err)
		return
	}

	conn, err := grpc.NewClient(
		apiAddress,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})),
	)
	if err != nil {
		initErr = fmt.Errorf("failed to create gRPC client: %w", err)
		return
	}
	apiConn = conn

	apiClient := application_apiv1.NewApplicationServiceClient(apiConn)
	eventHandler := apphandler.NewHandler(apiClient, authenticator)

	server := webhook.NewServer("", publicKey, eventHandler, webhook.WithSyncEventHandling())
	eventHandlerFn = server.EventHandlerFunc()

	log.Printf("setup completed")
}

// Handler is the Vercel entrypoint.
func Handler(w http.ResponseWriter, r *http.Request) {
	// 動作確認用
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	log.Printf("request: method=%s path=%s", r.Method, r.URL.Path)

	once.Do(setup)

	if initErr != nil {
		log.Printf("initialization error: %v", initErr)
		http.Error(w, "initialization failed", http.StatusInternalServerError)
		return
	}

	eventHandlerFn(w, r)
}
