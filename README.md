# geektutu ———— RPC框架GeeRPC学习

## 客户端设计

### 调用信息

封装结构体Call存储一次调用所需要的信息

```go
    type Call struct {
        Seq           uint64
        ServiceMethod string // "service.method"
        Args          interface{}
        Reply         interface{}
        Error         error
        Done          chan *Call // 用于支持异步调用 传递调用结束的信号
    }
```

一个调用无论是否正常结束，都需要在结束时调用Call.done()将自身（call对象）传递给Done，以表示调用结束。

```go
    // 通知调用方调用结束
    func (call *Call) done() {
        call.Done <- call
    }
```

### 客户端结构

```go
    type Client struct {
        cc       codec.Codec // 消息编解码器 序列化发送的请求，反序列化收到的响应
        opt      *Option
        sending  sync.Mutex // 确保请求有序发送，防止多个请求报文混淆
        header   codec.Header
        mu       sync.Mutex       // 并发情况下一个变量可能同时被多个goroutine修改，因此使用互斥锁
        seq      uint64           // 每个请求的唯一编号，递增
        pending  map[uint64]*Call // 存储未处理完的请求
        closing  bool             // 用户主动关闭客户端
        shutdown bool             // 有错误导致客户端关闭
    }
```

每个客户端需要一个消息编解码器用于序列化发送的请求，反序列化收到的响应。一个编解码器```codec.Codec```绑定一个连接。
为了确保客户端发送请求时报文不混淆，因此使用```sengding```互斥锁。
客户端维护一个序列号，每个请求发送前需要先创建一个Call对象并从客户端获取一个唯一序列号。
```mu```互斥锁用于保证并发修改客户端某些属性时的并发安全。

### 创建客户端

```go
    // network：通信方式TCP/UDP
    func Dail(network, address string, opts ...*Option) (client *Client, err error) {
        opt, err := parseOptions(opts...)
        if err != nil {
            return nil, err
        }
        conn, err := net.Dial(network, address) // 建立连接的方式
        if err != nil {
            return nil, err
        }
        defer func() {
            if client == nil { // 创建客户端失败则关闭net.Dail建立的连接
                _ = conn.Close()
            }
        }()
        return NewClient(conn, opt)
    }
```

实现了Dail方法创建客户端，传递的参数包括底层网络通信方式network，通信地址address以及编码报文方式的信息。

```go
    func NewClient(conn net.Conn, opt *Option) (*Client, error) {
        // 首先需要向服务端发送option协商报文编码方式
        f := codec.NewCodecFuncMap[opt.CodecType]
        if f == nil {
            err := fmt.Errorf("invalid codec type %s", opt.CodecType)
            log.Println("rpc client: codec error: ", err)
            return nil, err
        }
        // 必须成功发送option才能成功创建客户端
        if err := json.NewEncoder(conn).Encode(opt); err != nil {
            log.Println("rpc client: options error: ", err)
            _ = conn.Close()
            return nil, err
        }
        return newClientCodec(f(conn), opt), nil
    }
```

首先根据编码方式确定客户端的编码器构造函数，然后发送编码方式信息与服务端确定双方报文编码方式，只有确定后才能正式创建客户端。

```go
    func newClientCodec(cc codec.Codec, opt *Option) *Client {
        client := &Client{
            seq:     1, // 初始值
            cc:      cc,
            opt:     opt,
            pending: make(map[uint64]*Call),
        }
        go client.receive() // 异步接收响应
        return client
    }
```

创建客户端实例后直接开启一个goroutine用于客户端异步接收服务端的响应。

### 客户端发送请求

客户端封装了Call方法用于同步发送请求。

```go
    func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
        if done == nil {
            done = make(chan *Call, 1)
        } else if cap(done) == 0 {
            log.Panic("rpc client: done channel is unbuffered")
        }
        call := &Call{
            ServiceMethod: serviceMethod,
            Args:          args,
            Reply:         reply,
            Done:          done,
        }
        client.send(call)
        return call // 异步体现在没有等待调用完成：call.done()
    }

    // 同步RPC调用
    func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
        // 阻塞等待调用完成
        call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done // 阻塞直到执行了 call.done()
        return call.Error
    }
```

Go方法属于异步调用，因为在执行client.send发送请求后函数直接return，没有等待调用完成（send方法为非阻塞函数）。用户只需要传递ServiceMethod服务名和函数名、Args参数以及Reply接收响应的对象。因此首先构建的Call对象只包含这些信息，需要向客户端申请请求序列化。
每个调用执行结束后会调用Call.done()方法向Call对象的Done通道传递自身，因此同步RPC调用函数Call则复用了Go函数然后**阻塞等待**调用对象的Done通道传递调用结束的信号。

```go
    func (client *Client) send(call *Call) {
        client.sending.Lock()
        defer client.sending.Unlock()

        seq, err := client.registerCall(call)
        if err != nil {
            call.Error = err
            call.done()
            return
        }

        // 初始化请求头
        client.header.Seq = seq
        client.header.ServiceMethod = call.ServiceMethod
        client.header.Error = ""

        // 编码——发送 body为请求参数
        if err := client.cc.Write(&client.header, call.Args); err != nil {
            call := client.removeCall(seq)
            if call != nil {
                call.Error = err
                call.done()
            }
        }
    }
```

发送请求前客户端需要赋予每个请求一个唯一序列号，```registerCall```方法的作用就是如此，并且会将要发送的请求的调用信息添加到客户端的```pending```中，```pending```记录了所有未执行完成的RPC调用。
初始化请求头部信息后，客户端直接通过编码器发送了请求头部信息和RPC调用的参数。```removeCall(seq)```方法是根据序列号获取对应RPC调用的调用信息的方法，并且会将该调用从客户端的```pending```对象中删除，表示调用以及执行结束。

### 客户端接收响应

```go
    func (client *Client) receive() {
        var err error
        for err == nil {
            var h codec.Header
            if err = client.cc.ReadHeader(&h); err != nil {
                break
            }
            call := client.removeCall(h.Seq) // 取出Call信息处理调用情况
            switch {                         // 报文格式为 Header|body 因此无论如何对每个请求都要依次读取Header和body
            case call == nil: // 请求发送不完整或者被取消，但服务端仍然处理了
                err = client.cc.ReadBody(nil)
            case h.Error != "": // 服务端处理出错
                call.Error = fmt.Errorf(h.Error)
                err = client.cc.ReadBody(nil)
                call.done() // 处理完call
            default: // 正常处理
                err = client.cc.ReadBody(call.Reply)
                if err != nil {
                    call.Error = errors.New("reading body " + err.Error())
                }
                call.done() // 处理完call
            }
        }
        // 出错
        client.terminateCall(err)
    }
```

```receive```方法是创建客户端后异步执行的，客户端依赖这个goroutine循环地从与服务端的连接中获取服务端的响应消息。响应报文也分为头部和正文，每次接收响应时客户端首先获取响应头部信息，根据其中的序列号获取对应的调用信息。可能会有两种异常情况：客户端已经删除调用信息但服务端仍返回了该调用的响应、服务端处理出错。为了保证从连接正确有序地读取信息，碰到上述两种请求时仍要从连接中读取报文正文body。
```terminateCall```方法是客户端接收响应过程中发现错误时执行，此时意味着要关闭客户端连接。
