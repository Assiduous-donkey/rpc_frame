package main

import (
	"fmt"
	"geerpc"
	"log"
	"net"
	"sync"
	"time"
)

func startServer(addr chan string) {
	l, err := net.Listen("tcp", ":8000")
	if err != nil {
		log.Fatal("network error: ", err)
	}
	log.Println("start rpc server on", l.Addr())
	addr <- l.Addr().String()
	geerpc.Accept(l)
}

func main() {
	addr := make(chan string) // 确保服务端端口监听成功，客户端再发起请求
	go startServer(addr)

	client, _ := geerpc.Dail("tcp", <-addr)
	defer client.Close()

	time.Sleep(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 5; i += 1 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf("geerpc req %d", i)
			var reply string
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error: ", err)
			}
			log.Println("reply: ", reply)
		}(i)
	}
	wg.Wait()
}
