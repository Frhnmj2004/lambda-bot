package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"

	"lambda_bot/pkg/protogen"
)

// Config holds environment configuration
type Config struct {
	Port                 string
	VerifyToken          string
	AppSecret            string
	ApiToken             string
	PhoneNumberID        string
	OrchestratorGRPCAddr string
}

func loadConfig() *Config {
	return &Config{
		Port:                 os.Getenv("PORT"),
		VerifyToken:          os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		AppSecret:            os.Getenv("WHATSAPP_APP_SECRET"),
		ApiToken:             os.Getenv("WHATSAPP_API_TOKEN"),
		PhoneNumberID:        os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		OrchestratorGRPCAddr: os.Getenv("ORCHESTRATOR_GRPC_ADDR"),
	}
}

// WhatsApp Webhook Payload Structs (simplified for audio messages)
type WebhookPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From  string `json:"from"`
					ID    string `json:"id"`
					Type  string `json:"type"`
					Audio *struct {
						ID string `json:"id"`
					} `json:"audio,omitempty"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func validateSignature(appSecret string, body []byte, signatureHeader string) bool {
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signatureHeader))
}

func downloadWhatsAppMedia(mediaID, apiToken string) (string, error) {
	// Step 1: Get media URL
	metaURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s", mediaID)
	req, err := http.NewRequest("GET", metaURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("failed to get media url: %s", resp.Status)
	}
	var meta struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", err
	}

	// Step 2: Download media
	req2, err := http.NewRequest("GET", meta.URL, nil)
	if err != nil {
		return "", err
	}
	req2.Header.Set("Authorization", "Bearer "+apiToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		return "", fmt.Errorf("failed to download media: %s", resp2.Status)
	}
	// Save to /tmp/{mediaID}.ogg
	tmpDir := os.TempDir()
	filePath := filepath.Join(tmpDir, mediaID+".ogg")
	f, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(f, resp2.Body)
	if err != nil {
		return "", err
	}
	return filePath, nil
}

func sendWhatsAppReply(apiToken, phoneNumberID, recipientID, messageText string) error {
	url := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", phoneNumberID)
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                recipientID,
		"text":              map[string]string{"body": messageText},
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send reply: %s, %s", resp.Status, string(body))
	}
	return nil
}

func webhookHandler(cfg *Config, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			mode := r.URL.Query().Get("hub.mode")
			challenge := r.URL.Query().Get("hub.challenge")
			verifyToken := r.URL.Query().Get("hub.verify_token")
			if mode == "subscribe" && verifyToken == cfg.VerifyToken {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(challenge))
				return
			}
			w.WriteHeader(http.StatusForbidden)
			return
		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				logger.Printf("failed to read body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sig := r.Header.Get("X-Hub-Signature-256")
			if !validateSignature(cfg.AppSecret, body, sig) {
				logger.Println("invalid signature")
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var payload WebhookPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				logger.Printf("failed to unmarshal payload: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for _, entry := range payload.Entry {
				for _, change := range entry.Changes {
					for _, msg := range change.Value.Messages {
						if msg.Type == "audio" && msg.Audio != nil {
							from := msg.From
							mediaID := msg.Audio.ID
							logger.Printf("Received audio message from %s with media ID %s", from, mediaID)
							// Download media
							filePath, err := downloadWhatsAppMedia(mediaID, cfg.ApiToken)
							if err != nil {
								logger.Printf("failed to download media: %v", err)
								sendWhatsAppReply(cfg.ApiToken, cfg.PhoneNumberID, from, "Sorry, failed to download your audio.")
								continue
							}
							// Call orchestrator
							conn, err := grpc.Dial(cfg.OrchestratorGRPCAddr, grpc.WithInsecure())
							if err != nil {
								logger.Printf("failed to connect to orchestrator: %v", err)
								sendWhatsAppReply(cfg.ApiToken, cfg.PhoneNumberID, from, "Sorry, failed to process your audio.")
								continue
							}
							client := protogen.NewOrchestratorClient(conn)
							ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
							defer cancel()
							resp, err := client.ProcessVoiceMessage(ctx, &protogen.ProcessRequest{
								SenderId:      from,
								MediaId:       mediaID,
								AudioFilePath: filePath,
							})
							conn.Close()
							if err != nil {
								logger.Printf("orchestrator error: %v", err)
								sendWhatsAppReply(cfg.ApiToken, cfg.PhoneNumberID, from, "Sorry, failed to process your audio.")
								continue
							}
							sendWhatsAppReply(cfg.ApiToken, cfg.PhoneNumberID, from, resp.Summary)
						}
					}
				}
			}
			w.WriteHeader(http.StatusOK)
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func main() {
	cfg := loadConfig()
	logger := log.New(os.Stdout, "[whatsapp-gateway] ", log.LstdFlags)

	http.HandleFunc("/webhook", webhookHandler(cfg, logger))
	addr := ":" + cfg.Port
	logger.Printf("Starting server on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Fatalf("server failed: %v", err)
	}
}
