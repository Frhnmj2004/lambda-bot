package main

import (
	"context"
	"log"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"lambda_bot/pkg/protogen"
)

type orchestratorServer struct {
	protogen.UnimplementedOrchestratorServer
	audioClient      protogen.AudioProcessorClient
	summarizerClient protogen.TextSummarizerClient
}

func (s *orchestratorServer) ProcessVoiceMessage(ctx context.Context, req *protogen.ProcessRequest) (*protogen.ProcessResponse, error) {
	log.Printf("Processing voice message for sender %s, media_id %s", req.SenderId, req.MediaId)
	// Step 1: Transcribe audio
	transcribeResp, err := s.audioClient.TranscribeAudio(ctx, &protogen.TranscriptionRequest{
		AudioFilePath: req.AudioFilePath,
		MessageId:     req.MediaId,
	})
	if err != nil {
		log.Printf("Transcription failed: %v", err)
		return nil, status.Errorf(codes.Internal, "audio processor error: %v", err)
	}
	// Step 2: Summarize text
	summaryResp, err := s.summarizerClient.SummarizeText(ctx, &protogen.SummarizationRequest{
		Transcript: transcribeResp.Transcript,
		MessageId:  req.MediaId,
	})
	if err != nil {
		log.Printf("Summarization failed: %v", err)
		return nil, status.Errorf(codes.Internal, "text summarizer error: %v", err)
	}
	// Step 3: Cleanup
	if req.AudioFilePath != "" {
		if err := os.Remove(req.AudioFilePath); err != nil {
			log.Printf("Failed to delete temp file %s: %v", req.AudioFilePath, err)
		}
	}
	return &protogen.ProcessResponse{
		Summary: summaryResp.Summary,
	}, nil
}

func main() {
	// Connect to downstream services
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	audioConn, err := grpc.DialContext(ctx, "audio-processor:50051", grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("failed to connect to audio-processor: %v", err)
	}
	defer audioConn.Close()
	summarizerConn, err := grpc.DialContext(ctx, "text-summarizer:50052", grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		log.Fatalf("failed to connect to text-summarizer: %v", err)
	}
	defer summarizerConn.Close()

	grpcServer := grpc.NewServer()
	protogen.RegisterOrchestratorServer(grpcServer, &orchestratorServer{
		audioClient:      protogen.NewAudioProcessorClient(audioConn),
		summarizerClient: protogen.NewTextSummarizerClient(summarizerConn),
	})

	lis, err := net.Listen("tcp", ":50053")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	log.Println("orchestrator gRPC server listening on :50053")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
