package main

import (
	"testing"

	"github.com/bubustack/bubu-sdk-go/conformance"
	"github.com/bubustack/silero-vad-engram/pkg/config"
	"github.com/bubustack/silero-vad-engram/pkg/engram"
)

func TestStreamConformance(t *testing.T) {
	suite := conformance.StreamSuite[config.Config]{
		Engram: engram.New(),
		Config: config.Config{},
	}
	suite.Run(t)
}
