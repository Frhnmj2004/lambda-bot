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

const masterPrompt = `You are an expert meeting assistant. Your job is to summarize the following transcript for a busy executive. 

ROLE: Meeting Summarizer
CONTEXT: The transcript is from a business meeting. Identify key points, action items, and overall sentiment.
TASK:
1. List Key Points (bulleted)
2. List Action Items (bulleted, with responsible person if possible)
3. Summarize Sentiment (one sentence)
CONSTRAINTS: Limit summary to 150 words. Use clear, professional language. Format as Markdown.`

type textSummarizerServer struct {
	protogen.UnimplementedTextSummarizerServer
	geminiModel *genai.GenerativeModel
}

func (s *textSummarizerServer) SummarizeText(ctx context.Context, req *protogen.SummarizationRequest) (*protogen.SummarizationResponse, error) {
	log.Printf("Received summarization request for message: %s", req.GetMessageId())

	fullPrompt := fmt.Sprintf(masterPrompt+"\n\nTRANSCRIPT:\n%s", req.GetTranscript())

	resp, err := s.geminiModel.GenerateContent(ctx, genai.Text(fullPrompt))
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary from Gemini: %w", err)
	}

	// Extract text from the response
	var summary string
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if txt, ok := part.(genai.Text); ok {
				summary += string(txt)
			}
		}
	}

	if summary == "" {
		return nil, fmt.Errorf("Gemini returned an empty summary")
	}

	log.Printf("Successfully summarized text for message: %s", req.GetMessageId())
	return &protogen.SummarizationResponse{Summary: summary, MessageId: req.GetMessageId()}, nil
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
	protogen.RegisterTextSummarizerServer(grpcServer, &textSummarizerServer{geminiModel: model})

	lis, err := net.Listen("tcp", ":50052")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	log.Println("text-summarizer gRPC server listening on :50052")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
