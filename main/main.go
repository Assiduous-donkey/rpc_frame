package main

import (
	"context"
	"geerpc"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

func reflectDemo() {
	var wg sync.WaitGroup
	typ := reflect.TypeOf(&wg)

	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		argv := make([]string, 0, method.Type.NumIn())
		returns := make([]string, 0, method.Type.NumOut())
		for j := 1; j < method.Type.NumIn(); j++ {
			argv = append(argv, method.Type.In(j).Name())
		}
		for j := 1; j < method.Type.NumOut(); j++ {
			returns = append(returns, method.Type.Out(j).Name())
		}
		log.Printf("func (w *%s) %s(%s) %s",
			typ.Elem().Name(), // Go语言程序中对指针获取反射对象时，可以通过 reflect.Elem() 方法获取这个指针指向的元素类型
			method.Name,
			strings.Join(argv, ","),
			strings.Join(returns, ","),
		)
	}
}

type Foo int
type Args struct{ Num1, Num2 int }

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func startServer(addr chan string) {
	var foo Foo
	if err := geerpc.Register(foo); err != nil {
		log.Fatal("register error: ", err)
	}

	l, err := net.Listen("tcp", ":8000")
	if err != nil {
		log.Fatal("network error: ", err)
	}
	log.Println("start rpc server on", l.Addr())
	addr <- l.Addr().String()
	geerpc.Accept(l)
}

func rpcDemo() {
	addr := make(chan string)
	go startServer(addr)
	client, _ := geerpc.Dial("tcp", <-addr)
	defer client.Close()

	time.Sleep(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			ctx, _ := context.WithTimeout(context.Background(), time.Second)
			if err := client.Call(ctx, "Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error: ", err)
			}
			log.Printf("%d + %d = %d\n", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}

func chDemo() {
	ch := make(chan struct{}, 1)
	go func() {
		time.Sleep(time.Second * 3)
		ch <- struct{}{}
		log.Println("ch<-struct{}{} done")
	}()
	select {
	case <-time.After(time.Second):
		log.Println("timeout")
	case <-ch:
		log.Println("<-ch done")
	}
	// time.Sleep(time.Second * 5)
}

func main() {
	// reflectDemo()
	// rpcDemo()
	// defer func() {
	// 	fmt.Println("the number of goroutines: ", runtime.NumGoroutine())
	// }()
	// chDemo()
	// time.Sleep(time.Second * 5)
}
