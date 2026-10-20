package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type config struct {
	Frames          []byte
	FinalFrames     []byte
	Stderr          string
	ExitCode        int
	ExitImmediately bool
	IgnoreStop      bool
	InterruptMarker string
}

func main() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		panic(err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path+".args", []byte(strings.Join(os.Args[1:], "\n")), 0600); err != nil {
		panic(err)
	}
	if cfg.InterruptMarker != "" {
		marker, err := os.OpenFile(cfg.InterruptMarker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			marker.Close()
			fmt.Fprintln(os.Stderr, "Capture interrupted. Device changed")
			os.Exit(2)
		}
		if !os.IsExist(err) {
			panic(err)
		}
	}
	fmt.Fprint(os.Stderr, cfg.Stderr)
	os.Stdout.Write(cfg.Frames)
	if cfg.ExitImmediately {
		os.Exit(cfg.ExitCode)
	}
	if cfg.IgnoreStop {
		for {
			time.Sleep(time.Hour)
		}
	}
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Stdout.Write(cfg.FinalFrames)
}
