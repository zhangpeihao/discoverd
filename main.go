package main

import (
	"flag"
	. "github.com/zhangpeihao/discoverd/daemon"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

var (
	etcdAddress         *string = flag.String("etcd", "http://127.0.0.1:2379,http://configsrv:2379", "etcd servers (comma separated)")
	serverId            *string = flag.String("serverid", "", "server ID (leave it empty to use hostname as server ID)")
	prefix              *string = flag.String("prefix", "", "etcd key prefix")
	updateStateInterval *int    = flag.Int("UpdateStateInterval", 20, "The update state interval on etcd. TTL is 3 times of this value.")
)

func main() {
	flag.Parse()

	if len(*serverId) == 0 {
		if hostname, err := os.Hostname(); err != nil {
			log.Println(os.Stderr, "Get hostname err: ", err)
			os.Exit(1)
		} else {
			*serverId = hostname
		}
	}

	params := &DaemonParameters{
		EtcdAddresses:       strings.Split(*etcdAddress, ","),
		ServerId:            *serverId,
		EtcdKeyPrefix:       *prefix,
		UpdateStateInterval: uint64(*updateStateInterval),
	}

	daemon := NewDaemon(params)
	daemon.Run()

	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT)
	sig := <-ch
	log.Printf("Signal received: %v\n", sig)
	os.Exit(0)
}
