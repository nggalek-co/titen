package main

import (
	"os"
	"titen/internal/branding"
)

func main() {
	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	icon, err := branding.Icon(src)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Args[2], icon, 0644); err != nil {
		panic(err)
	}
}
