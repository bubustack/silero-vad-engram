package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/bubustack/bubu-sdk-go"
	vadengram "github.com/bubustack/silero-vad-engram/pkg/engram"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := sdk.StartStreaming(ctx, vadengram.New()); err != nil {
		log.Fatalf("silero-vad engram failed: %v", err)
	}
}
