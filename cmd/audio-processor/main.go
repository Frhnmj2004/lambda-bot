package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
	"google.golang.org/grpc"

	"lambda_bot/pkg/protogen"
)

type audioProcessorServer struct {
	protogen.UnimplementedAudioProcessorServer
	geminiModel *genai.GenerativeModel
}

func (s *audioProcessorServer) TranscribeAudio(ctx context.Context, req *protogen.TranscriptionRequest) (*protogen.TranscriptionResponse, error) {
	log.Printf("Received transcription request for message: %s", req.GetMessageId())

	audioFilePath := req.GetAudioFilePath()
	audioData, err := os.ReadFile(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file %s: %w", audioFilePath, err)
	}
	defer os.Remove(audioFilePath) // Clean up the temp file

	prompt := genai.Text("Transcribe this voice message accurately. Preserve the conversational nature.")
	audioPart := genai.Blob{MIMEType: "audio/ogg", Data: audioData}

	resp, err := s.geminiModel.GenerateContent(ctx, audioPart, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate content from Gemini: %w", err)
	}

	// Extract text from the response
	var transcript string
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				transcript += string(txt)
			}
		}
	}

	if transcript == "" {
		return nil, fmt.Errorf("Gemini returned an empty transcript")
	}

	log.Printf("Successfully transcribed audio for message: %s", req.GetMessageId())
	return &protogen.TranscriptionResponse{Transcript: transcript, MessageId: req.GetMessageId()}, nil
}

func main() {
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	if err != nil {
		log.Fatalf("Failed to create Gemini client: %v", err)
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")
	grpcServer := grpc.NewServer()
	protogen.RegisterAudioProcessorServer(grpcServer, &audioProcessorServer{geminiModel: model})

	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	log.Println("audio-processor gRPC server listening on :50051")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
