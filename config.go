package main

import (
	"os"
)

var (
	signalingServerOrigin = os.Getenv("SIGNALING_SERVER_ORIGIN")
	applicationServerOrigin = os.Getenv("APPLICATION_SERVER_ORIGIN")
)