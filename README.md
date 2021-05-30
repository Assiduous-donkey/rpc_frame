# geektutu ———— RPC框架GeeRPC学习

## 超时处理

实现方式就是异步执行原来的代码逻辑，通过channel传递代码块执行结束的信号，并设置计时器等待是否超时。

在整个RPC过程中，需要客户端处理超时的地方有：

1. 与服务端建立连接时超时
2. 发送请求到服务端，在写报文时超时
3. 等待服务端处理时，等待响应的超时
4. 从服务端接收响应，在读报文时超时

需要服务端处理的超时有：

1. 读取客户端请求，在读报文时超时
2. 发送响应报文，在写报文时超时
3. 调用映射服务的方法时超时

### GeeRPC的超时处理机制

GeeRPC在三个地方添加了超时处理机制

#### 客户端创建连接时

在```Option```结构体中添加了```ConnectTimeout```字段用于表示客户端创建连接时的超时时间（0表示不做限制）。

```go
    type clientResult struct {  // 用于channel传递客户端的创建结果
        client *Client
        err    error
    }

    type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

    func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
        opt, err := parseOptions(opts...)
        if err != nil {
            return nil, err
        }
        conn, err := net.DialTimeout(network, address, opt.ConnectTimeout) // 客户端连接建立是否超时
        if err != nil {
            return nil, err
        }
        defer func() {
            if err != nil {
                _ = conn.Close()
            }
        }()
        // newClientFunc的执行是否超时（包括了发送编码方法给服务端）
        ch := make(chan clientResult)
        go func() {
            client, err := f(conn, opt)
            ch <- clientResult{client: client, err: err}
        }()
        if opt.ConnectTimeout == 0 {    // 不限制时间
            result := <-ch
            return result.client, result.err
        }
        select {
        case <-time.After(opt.ConnectTimeout):
            return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
        case result := <-ch:
            return result.client, result.err
        }
    }

    func Dial(network, address string, opts ...*Option) (client *Client, err error) {
        return dialTimeout(NewClient, network, address, opts...)
    }
```

包括检查两部分逻辑是否超时。一部分是根据通信方式和地址建立与服务端的连接，采用了```net.DialTimeout```方法，将```Option.ConnectTimeout```作为参数。另一部分是创建客户端结构体的时间，包括与服务端协商报文编码方式以及构造```Client```结构体。

#### 客户端执行Call方法时

```go
    // 同步RPC调用 使用context由用户控制RPC调用的超时时间
    func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
        // 阻塞等待调用完成
        call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
        select {
        case <-ctx.Done():
            client.removeCall(call.Seq)
            return errors.New("rpc client: call failed: " + ctx.Err().Error())
        case call := <-call.Done:
            return call.Error
        }
    }
```

在客户端执行RPC的方法```Call```新增参数```ctx context.Context```，将客户端要求的RPC超时时间的设置权交给用户。用户可以通过```context.WithTimeout```方法创建携带超时时间的```context.Context```。在```client.Call```中，执行为异步调用后监听是否超时，是的话从客户端中移除掉本次调用的信息。

在此处添加超时处理机制，处理的情况包括了客户端发送报文超时、等待响应超时、处理响应超时，相当于要求客户端发送报文的时间+等待响应的时间+处理响应的时间小于用户设置的超时时间。

#### 服务端处理请求时

```go
    /* ************************************
                作者的版本
    ************************************ */
    func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
        defer wg.Done()
        called := make(chan struct{})
        sent := make(chan struct{})
        go func() {
            err := req.svc.call(req.mtype, req.argv, req.replyv)
            called <- struct{}{}
            if err != nil {
                req.h.Error = err.Error()
                server.sendResponse(cc, req.h, invalidRequest, sending)
                sent <- struct{}{}
                return
            }
            server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
            sent <- struct{}{}
        }()

        if timeout == 0 {
            <-called
            <-sent
            return
        }
        select {
        case <-time.After(timeout):
            req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
            server.sendResponse(cc, req.h, invalidRequest, sending)
        case <-called:
            <-sent
        }
    }
```

**这个地方作者的逻辑有goroutine泄露的风险**。
原版代码采用了```called```和```sent```两个channel来传递调用方法结束的信号和发送响应的信号，由于```called```是无缓冲channel，所以当调用超时，即满足```<-time.After(timeout)```这个case时，函数```handleRequest```很快执行完成，而其开启的goroutine还未执行完，等到该gouroutine执行完调用的方法后要传递信号```called <- struct{}{}```时已经没有```called```这个通道的接收者，因此该goroutine会阻塞在这行代码，无法释放，从而导致goroutine泄露。

**采用带缓冲的channel可以解决这个问题**。
即使超时，上述goroutine在执行完调用的方法后也能向```called```通道写入信号（因为该通道带缓存），但后续发送响应后执行的```sent <- struct{}{}```一样会阻塞。所以我修改了这部分的逻辑，代码如下：

```go
    /* ************************************
                我的版本
    ************************************ */
    func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
        // 等待同一个连接的多个请求处理完再关闭连接
        defer wg.Done()

        // called设置为带缓存的channel 是为了防止超时情况下下面的goroutine阻塞在called<-struct{}{}导致无法退出 因为此时主函数已经退出
        called := make(chan error, 1) // 传递RPC调用结束信号
        go func() {
            err := req.svc.call(req.mtype, req.argv, req.replyv) // 调用RPC方法
            called <- err
        }()

        if timeout == 0 { // 不限制是否超时
            if err := <-called; err != nil {
                req.h.Error = err.Error()
                server.sendResponse(cc, req.h, invalidRequest, sending)
            } else {
                server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
            }
            return
        }
        select {
        case <-time.After(timeout):
            req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
            server.sendResponse(cc, req.h, invalidRequest, sending)
        case err := <-called:
            if err != nil {
                req.h.Error = err.Error()
                server.sendResponse(cc, req.h, invalidRequest, sending)
            } else {
                server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
            }
        }
    }
```

将```called```通道用于传递调用执行完成的信号并携带调用结果，去掉```sent```通道，根据调用结果决定发送的响应。虽然这种写法会有部分代码重复，但可以解决上述问题。
