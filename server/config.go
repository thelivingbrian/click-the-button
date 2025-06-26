package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Configuration struct {
	port              string
	pprofEnabled      bool
	pprofPort         string
	snapshotInterval  time.Duration
	broadcastInterval time.Duration
}

func getConfiguration() *Configuration {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
		return nil
	}

	si := os.Getenv("SNAPSHOT_INTERVAL")
	snapshotInterval, err := time.ParseDuration(si)
	if err != nil {
		fmt.Printf("Invalid SNAPSHOT_INTERVAL=%q, defaulting to 0: %v\n", si, err)
		snapshotInterval = 0
	}

	bi := os.Getenv("BROADCAST_INTERVAL")
	broadcastInterval, err := time.ParseDuration(bi)
	if err != nil {
		fmt.Printf("Invalid BROADCAST_INTERVAL=%q, defaulting to 0: %v\n", bi, err)
		broadcastInterval = 0
	}

	config := Configuration{
		port:              os.Getenv("PORT"),
		pprofEnabled:      strings.ToLower(os.Getenv("PPROF_ENABLED")) == "true",
		pprofPort:         os.Getenv("PPROF_PORT"),
		snapshotInterval:  snapshotInterval,
		broadcastInterval: broadcastInterval,
	}
	return &config
}

func (config *Configuration) snapshotEnabled() bool {
	return config.snapshotInterval != 0
}

func (config *Configuration) broadcastEnabled() bool {
	return config.broadcastInterval != 0
}
