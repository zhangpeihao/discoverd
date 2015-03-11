package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		fmt.Println("Usage: sleep <seconds>")
		os.Exit(1)
	}
	seconds, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Println("Usage: sleep <seconds>")
		os.Exit(1)
	}
	exit_chan := make(chan bool)
	go func() {
		time.Sleep(time.Duration(seconds) * time.Second)
		exit_chan <- true
	}()
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT)
	for {
		select {
		case sig := <-ch:
			fmt.Printf("Signal received: %v\n", sig)
			os.Exit(0)
		case <-exit_chan:
			fmt.Printf("timeout: %d\n", seconds)
			os.Exit(0)
		}
	}
}
