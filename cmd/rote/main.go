package main

import (
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("rote %s\n", version)
	case "list":
		fmt.Println("TODO: list jobs")
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "rote run: missing job name")
			os.Exit(2)
		}
		fmt.Printf("TODO: run job %s\n", os.Args[2])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rote <command> [arguments]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  version       print the rote version")
	fmt.Fprintln(os.Stderr, "  list          list configured jobs")
	fmt.Fprintln(os.Stderr, "  run <job>     run a job by name")
}
