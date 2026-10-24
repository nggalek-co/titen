package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
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
	Interruptions   int
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
	if cfg.Interruptions > 0 {
		counterPath := path + ".interruptions"
		var count int
		if raw, err := os.ReadFile(counterPath); err == nil {
			count, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		if count < cfg.Interruptions {
			if err := os.WriteFile(counterPath, []byte(strconv.Itoa(count+1)), 0600); err != nil {
				panic(err)
			}
			fmt.Fprintln(os.Stderr, "The audio device changed or disconnected. Start recording again.")
			os.Exit(2)
		}
	}
	fmt.Fprint(os.Stderr, cfg.Stderr)
	if err := write(cfg.Frames); err != nil {
		panic(err)
	}
	if cfg.ExitImmediately {
		os.Exit(cfg.ExitCode)
	}
	if cfg.IgnoreStop {
		for {
			time.Sleep(time.Hour)
		}
	}
	bufio.NewReader(os.Stdin).ReadString('\n')
	if err := write(cfg.FinalFrames); err != nil {
		panic(err)
	}
}

func write(data []byte) error {
	for len(data) > 0 {
		n, err := os.Stdout.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
